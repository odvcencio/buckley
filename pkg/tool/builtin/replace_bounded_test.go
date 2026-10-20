package builtin

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var boundedReplacementPatterns = []string{
	`a`, `a+`, `a*`, `a*?`, `a|ab`, `aa|aab|b`, `^a|a`, `(?m)^a|a$`, `\Aa|a\z`,
	`\b`, `\B`, `\b(a+)\b`, `^|$`, `(?m)^|$`, `(?P<word>a+)|(b)`, `(a)?`,
	`(?i)(a)`, `(?U)(a+)`, `(界*)`, `(?P<1000000000>a)`, `(?s)(.*)`, `\Q`,
}

var boundedReplacementInputs = []string{"", "a", "aaa", "aaab", "baa", "xabc abc", "a\na\nb\n", "界a界", "界界", " a a ", "bbb", string([]byte{0xff, 'a', 0xfe})}

func TestRegexpScan_MatchesGoContextAndEmptyMatchRules(t *testing.T) {
	for _, pattern := range boundedReplacementPatterns {
		re := regexp.MustCompile(pattern)
		for _, input := range boundedReplacementInputs {
			scan, err := newRegexpScan(re, input)
			if err != nil {
				t.Fatal(err)
			}
			var got [][]int
			for match := scan.next(); match != nil; match = scan.next() {
				got = append(got, match)
			}
			want := re.FindAllStringSubmatchIndex(input, -1)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("pattern=%q input=%q: got=%v want=%v", pattern, input, got, want)
			}
		}
	}
}

func TestReplaceRegexpBounded_PreservesReplacementSemantics(t *testing.T) {
	for _, pattern := range boundedReplacementPatterns {
		re := regexp.MustCompile(pattern)
		for _, input := range boundedReplacementInputs {
			for _, replacement := range []string{"X", "$0$0", "${word}", "$1/${2}/$$", "$0x", "${", "$01", "${missing}", "$", "界${word}", "$1000000000"} {
				for _, maximum := range []int{0, 1, 2} {
					want := re.ReplaceAllString(input, replacement)
					matches := re.FindAllStringSubmatchIndex(input, -1)
					count := len(matches)
					if maximum > 0 {
						count = min(count, maximum)
						var output strings.Builder
						end := 0
						for _, match := range matches[:count] {
							output.WriteString(input[end:match[0]])
							output.Write(re.ExpandString(nil, replacement, input, match))
							end = match[1]
						}
						output.WriteString(input[end:])
						want = output.String()
					}
					got, gotCount, err := replaceRegexpBounded(re, input, replacement, maximum, 1<<20)
					if err != nil || got != want || gotCount != count {
						t.Fatalf("pattern=%q input=%q replacement=%q maximum=%d: got=%q/%d error=%v want=%q/%d", pattern, input, replacement, maximum, got, gotCount, err, want, count)
					}
					dry, dryCount, err := replaceRegexpBoundedMode(re, input, replacement, maximum, 1<<20, true)
					if err != nil || dry != input || dryCount != count {
						t.Fatalf("dry-run pattern=%q input=%q: got=%q/%d error=%v want=%q/%d", pattern, input, dry, dryCount, err, input, count)
					}
				}
			}
		}
	}
}

func FuzzRegexpScan_Context(f *testing.F) {
	for _, pattern := range boundedReplacementPatterns {
		f.Add(pattern, "aaab\n界a a", "$1$$${word}")
	}
	f.Fuzz(func(t *testing.T, pattern, input, replacement string) {
		if len(pattern) > 256 || len(input) > 128 || len(replacement) > 128 {
			t.Skip()
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			t.Skip()
		}
		scan, err := newRegexpScan(re, input)
		if err != nil {
			t.Fatal(err)
		}
		var got [][]int
		for match := scan.next(); match != nil; match = scan.next() {
			got = append(got, match)
		}
		if want := re.FindAllStringSubmatchIndex(input, -1); !reflect.DeepEqual(got, want) {
			t.Fatalf("pattern=%q input=%q: got=%v want=%v", pattern, input, got, want)
		}
		output, count, err := replaceRegexpBounded(re, input, replacement, 0, 1<<20)
		if err != nil || output != re.ReplaceAllString(input, replacement) || count != len(got) {
			t.Fatalf("pattern=%q input=%q replacement=%q: output=%q count=%d err=%v", pattern, input, replacement, output, count, err)
		}
	})
}
