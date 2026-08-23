Repair your prior candidate patch for the MIT-licensed github.com/odvcencio/gotreesitter repository at exact commit f8b9d718ee19f65598e274035f5481a899ab2b72.

Scope remains exactly grep/where.go and grep/where_test.go. Do not touch anything else.

Independent review found two concrete failures:

1. `git apply --check` fails with `error: corrupt patch at line 46`. The grep/where_test.go hunk incorrectly used `@@ -0,0 +1,79 @@` even though the file already exists. Produce a genuine unified diff with valid context against the existing files.
2. The quoted-newline test input contained an actual newline, but its expected value used a raw string containing the two characters backslash+n. The expected clause must preserve the actual newline.

Also strengthen the candidate so the public behavior is tested through CompileWhere, not only the private splitter. Cover quoted semicolon and quoted newline arguments, both single and double quotes, a backslash-escaped matching quote followed by a separator inside the same argument, and ordinary semicolon/newline multi-clause AND behavior. Keep the implementation small and account for whether a quote is escaped based on the preceding backslash run, so an even number of backslashes does not falsely escape it.

Return only a git-compatible unified diff that passes `git apply --check` against the exact base. No Markdown fences or explanation.

Your rejected patch was:

diff --git a/grep/where.go b/grep/where.go
--- a/grep/where.go
+++ b/grep/where.go
@@ -60,9 +60,30 @@ func CompileWhere(where string) (WhereFilter, error) {
 }
 
-// splitClauses splits a where string on semicolons and newlines.
+// splitClauses splits a where string on semicolons and newlines that occur
+// outside of single- or double-quoted arguments. Backslash-escaped quotes
+// inside a quoted argument do not terminate the quote.
 func splitClauses(s string) []string {
-	// Replace newlines with semicolons, then split.
-	s = strings.ReplaceAll(s, "\n", ";")
-	return strings.Split(s, ";")
+	var clauses []string
+	var cur strings.Builder
+	var quote byte
+	for i := 0; i < len(s); i++ {
+		c := s[i]
+		switch {
+		case quote != 0:
+			if c == '\\' && i+1 < len(s) && s[i+1] == quote {
+				cur.WriteByte(c)
+				cur.WriteByte(quote)
+				i++
+				continue
+			}
+			if c == quote {
+				quote = 0
+			}
+			cur.WriteByte(c)
+		case c == '"' || c == '\'':
+			quote = c
+			cur.WriteByte(c)
+		case c == ';' || c == '\n':
+			clauses = append(clauses, cur.String())
+			cur.Reset()
+		default:
+			cur.WriteByte(c)
+		}
+	}
+	clauses = append(clauses, cur.String())
+	return clauses
 }

Existing grep/where_test.go context around the desired insertion point:

func TestCompileWhere_MultipleClauses(t *testing.T) {
	// Two constraints joined by semicolon — both must pass.
	filter, err := CompileWhere(`contains($NAME, "Func"); not contains($NAME, "Test")`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Passes both: contains "Func" and does not contain "Test".
	r := &Result{
		Captures: map[string]Capture{
			"NAME": {Name: "NAME", Text: []byte("myFunc")},
		},
	}
	if !filter(r, nil, nil) {
		t.Error("expected filter to match myFunc")
	}

	// Fails second: contains "Test".
	r2 := &Result{
		Captures: map[string]Capture{
			"NAME": {Name: "NAME", Text: []byte("TestFunc")},
		},
	}
	if filter(r2, nil, nil) {
		t.Error("expected filter to NOT match TestFunc (contains Test)")
	}

	// Fails first: does not contain "Func".
	r3 := &Result{
		Captures: map[string]Capture{
			"NAME": {Name: "NAME", Text: []byte("myHelper")},
		},
	}
	if filter(r3, nil, nil) {
		t.Error("expected filter to NOT match myHelper (does not contain Func)")
	}
}

func TestCompileWhere_MissingCapture(t *testing.T) {
	filter, err := CompileWhere(`contains($MISSING, "hello")`)
