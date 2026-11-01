package policy

import "strings"

// maxShellNesting is how deep substitutions may nest inside one another. Past
// it the reader reports a match, the safe error, so hostile text cannot keep it
// busy.
const maxShellNesting = 16

// shellOperatorTail maps the start of a redirection operator to the bytes that
// can extend it: >> >| >& << <> <& <<< <<- &> &>>.
var shellOperatorTail = map[string]string{">": ">|&", "<": "<>&", "&": ">", "<<": "<-", "&>": ">"}

// anyShellSegment reports whether match accepts the words of any simple command
// in the line. See shellReader for how the line is read.
func anyShellSegment(command string, match func(words []string) bool) bool {
	spare := 4*len(command) + 4096
	r := &shellReader{src: command, match: match, spare: &spare}
	r.step(0)
	return r.read(0)
}

// shellReader reads command text the way bash does, far enough to find each
// simple command and its words. A plain split on spaces gets this wrong in many
// small ways, and each is a way to hide a command from the checks in this
// package. The reader follows bash on these points:
//
//   - Only the space and the tab end a word. A carriage return or a Unicode space
//     stays inside it.
//   - A backslash and newline vanish, also inside a word, but not in a comment
//     or in single quotes.
//   - A quoted empty word, such as "", is still a word.
//   - A # starts a comment only at the start of a word.
//   - A redirection and its target are not words of the command.
//   - A $(...), `...`, ${...}, <(...) or >(...) is one word, or part of one. The
//     text inside a $(...), ${...}, <(...) or >(...) outside quotes is a command
//     list of its own. A backquote, and any expansion inside double quotes, is
//     only skipped, not read for commands. Markdown in a here-document is full of
//     backquotes, and a commit message in a here-document has lines that start
//     with an agent's name, so reading them would deny harmless commands.
//
// The reader expands nothing. An expansion stays in its word as a "$", so the
// word counts as expanded.
type shellReader struct {
	src   string
	pos   int
	depth int
	// match receives the words of each simple command. A nil match only finds
	// where the text ends.
	match func(words []string) bool
	// param is set while reading the inside of ${...}, where only quotes,
	// escapes, expansions and the closing brace mean anything.
	param  bool
	closed bool // read stopped at the byte that closes a substitution
	// spare is how many bytes the readers of one line may still spend on
	// expansions inside double quotes that never close. Each one reads to the end
	// of the text, so without a limit a long run of "$(" would take quadratic time.
	spare *int

	words  []string // the words of the command being read
	word   []byte   // the word being read
	open   bool     // a word is open, even an empty one such as ""
	quoted bool     // the open word holds a quote, an escape, or an expansion
	target bool     // the next word is the target of a redirection
}

// read reads simple commands until the text ends or, for a substitution, until
// the byte that closes it, which is stop. It reports whether match accepted a
// command.
func (r *shellReader) read(stop byte) bool {
	groups := 0 // ( ) groups opened inside a $( ) reader
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		if r.param && !strings.ContainsRune("}\\'\"`$", rune(c)) {
			r.step(1)
			continue
		}
		switch c {
		case ' ', '\t':
			r.endWord()
			r.step(1)
		case '\n', ';', '|':
			if r.endCommand() {
				return true
			}
			r.step(1)
		case '&':
			if r.peek() == '>' {
				r.redirect()
				break
			}
			if r.endCommand() {
				return true
			}
			r.step(1)
		case '(':
			groups++
			if r.endCommand() {
				return true
			}
			r.step(1)
		case ')':
			if r.endCommand() {
				return true
			}
			r.step(1)
			if groups == 0 && stop == ')' {
				r.closed = true
				return false
			}
			groups = max(groups-1, 0)
		case '}':
			r.step(1)
			if stop == '}' {
				r.closed = true
				return false
			}
			r.add(c)
		case '#':
			if r.open {
				r.add(c)
				r.step(1)
			} else if end := strings.IndexByte(r.src[r.pos:], '\n'); end >= 0 {
				r.pos += end // the comment ends at the newline, whatever precedes it
			} else {
				r.pos = len(r.src)
			}
		case '\\':
			r.mark()
			if r.pos+1 < len(r.src) {
				r.add(r.src[r.pos+1])
				r.step(2)
			} else {
				r.add(c)
				r.step(1)
			}
		case '\'':
			r.singleQuoted()
		case '"':
			r.doubleQuoted()
		case '`', '$', '<', '>':
			if c != '`' && c != '$' && r.peek() != '(' {
				r.redirect()
				break
			}
			read, matched := r.expansion(false)
			if matched {
				return true
			}
			if !read {
				r.add(c)
				r.step(1)
			}
		default:
			r.add(c)
			r.step(1)
		}
	}
	return r.endCommand()
}

// skipContinuations returns the first index at or after i that does not start a
// backslash and newline. The shell removes that pair before it reads words.
func (r *shellReader) skipContinuations(i int) int {
	for i+1 < len(r.src) && r.src[i] == '\\' && r.src[i+1] == '\n' {
		i += 2
	}
	return i
}

// step moves the cursor n bytes on, past any line continuation.
func (r *shellReader) step(n int) {
	r.pos = min(r.skipContinuations(r.pos+n), len(r.src))
}

// peek returns the byte after the cursor, past any line continuation, or 0.
func (r *shellReader) peek() byte {
	if i := r.skipContinuations(r.pos + 1); i < len(r.src) {
		return r.src[i]
	}
	return 0
}

// add puts a byte in the open word, and opens one if none is.
func (r *shellReader) add(c byte) {
	r.word = append(r.word, c)
	r.open = true
}

// mark records that the word holds a quote, an escape or an expansion, which
// keeps a number from reading as a file descriptor.
func (r *shellReader) mark() {
	r.open, r.quoted = true, true
}

func (r *shellReader) clearWord() {
	r.word, r.open, r.quoted = r.word[:0], false, false
}

// endWord closes the open word. It is dropped when it is a redirection target.
func (r *shellReader) endWord() {
	if !r.open {
		return
	}
	if r.target {
		r.target = false
	} else {
		r.words = append(r.words, string(r.word))
	}
	r.clearWord()
}

// endCommand closes the command and reports whether match accepts its words.
func (r *shellReader) endCommand() bool {
	r.endWord()
	r.target = false
	words := r.words
	r.words = nil
	return r.match != nil && !r.param && len(words) > 0 && r.match(words)
}

// redirect reads a redirection operator. A word of digits just before it is a
// file descriptor, not an argument, and the word after it is the target. The
// operators >&- and <&- close a descriptor and take no target.
func (r *shellReader) redirect() {
	if r.open && !r.quoted && isDigits(r.word) {
		r.clearWord()
	} else {
		r.endWord()
	}
	op := r.src[r.pos : r.pos+1]
	r.step(1)
	for r.pos < len(r.src) && strings.IndexByte(shellOperatorTail[op], r.src[r.pos]) >= 0 {
		op += r.src[r.pos : r.pos+1]
		r.step(1)
	}
	r.target = true
	if (op == ">&" || op == "<&") && r.pos < len(r.src) && r.src[r.pos] == '-' {
		r.step(1)
		r.target = false
	}
}

func isDigits(word []byte) bool {
	for _, c := range word {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(word) > 0
}

// singleQuoted reads '...', which holds every byte as it is. A quote that never
// closes holds the rest of the text.
func (r *shellReader) singleQuoted() {
	r.mark()
	start := r.pos + 1
	end := strings.IndexByte(r.src[start:], '\'')
	if end < 0 {
		r.word = append(r.word, r.src[start:]...)
		r.pos = len(r.src)
		return
	}
	r.word = append(r.word, r.src[start:start+end]...)
	r.pos = start + end
	r.step(1)
}

// doubleQuoted reads "...". A backslash escapes only $, `, ", \ and a newline
// there. A $(...), ${...} or `...` inside is skipped, so a quote in it does not
// end the string.
func (r *shellReader) doubleQuoted() {
	r.mark()
	r.pos++
	for r.pos < len(r.src) {
		c := r.src[r.pos]
		switch {
		case c == '"':
			r.step(1)
			return
		case c == '\\' && r.pos+1 < len(r.src) && strings.IndexByte("$`\"\\\n", r.src[r.pos+1]) >= 0:
			if r.src[r.pos+1] != '\n' {
				r.add(r.src[r.pos+1])
			}
			r.pos += 2
		case c == '$' || c == '`':
			if read, _ := r.expansion(true); !read {
				r.add(c)
				r.pos++
			}
		default:
			r.add(c)
			r.pos++
		}
	}
}

// expansion reads the expansion that starts at the cursor: $(...), $((...)),
// ${...}, `...`, $'...' and, outside quotes, <(...) and >(...). It puts a "$" in
// the word in its place. It reports whether the cursor was at an expansion, and
// whether a command inside it matched.
//
// Only a $(...), ${...}, <(...) or >(...) outside quotes is read for commands.
// A backquote or an expansion inside double quotes is skipped, and it counts
// only if it closes. One that stays open, say a lone backquote in a Markdown code
// fence or an apostrophe in a here-document, must not swallow the rest of the
// text: it then reads as a plain character.
func (r *shellReader) expansion(inQuotes bool) (read, matched bool) {
	if inQuotes && *r.spare <= 0 {
		return false, false
	}
	c, next := r.src[r.pos], r.peek()
	var child *shellReader
	var stop byte
	switch {
	case c == '$' && next == '\'' && !inQuotes:
		r.ansiC()
		return true, false
	case c == '`':
		end, closed := r.backquoted()
		if !closed {
			if inQuotes {
				*r.spare -= end - r.pos
			}
			return false, false
		}
		r.mark()
		r.word = append(r.word, '$')
		r.pos = min(r.skipContinuations(end), len(r.src))
		return true, false
	case next == '(' && (c == '$' || !inQuotes), c == '$' && next == '{':
		child, stop = &shellReader{src: r.src}, ')'
		if next == '{' {
			child.param, stop = true, '}'
		}
		child.pos = r.skipContinuations(r.skipContinuations(r.pos+1) + 1)
	default:
		return false, false
	}
	if r.depth >= maxShellNesting {
		if inQuotes || r.match == nil {
			return false, false
		}
		r.pos = len(r.src)
		return true, true
	}
	child.depth, child.match, child.spare = r.depth+1, r.match, r.spare
	if inQuotes {
		child.match = nil
	}
	start := child.pos
	matched = child.read(stop)
	if inQuotes && !child.closed {
		*r.spare -= child.pos - start
		return false, false
	}
	r.mark()
	r.word = append(r.word, '$')
	r.pos = child.pos
	return true, matched
}

// backquoted returns the index just past the backquote that closes the one at
// the cursor. A backslash escapes the byte after it, so \` does not close it.
func (r *shellReader) backquoted() (end int, closed bool) {
	for i := r.pos + 1; i < len(r.src); i++ {
		switch r.src[i] {
		case '\\':
			i++
		case '`':
			return i + 1, true
		}
	}
	return len(r.src), false
}

// ansiC reads $'...', where a backslash escapes any byte, the quote included.
func (r *shellReader) ansiC() {
	r.mark()
	r.word = append(r.word, '$')
	r.pos = r.skipContinuations(r.pos+1) + 1
	for r.pos < len(r.src) {
		switch r.src[r.pos] {
		case '\\':
			r.pos++
		case '\'':
			r.step(1)
			return
		}
		r.pos++
	}
	r.pos = len(r.src)
}
