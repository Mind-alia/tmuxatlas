package secrets

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	text := "\xef\xbb\xbf# a comment\r\n" +
		"\n" +
		"   \t\n" +
		"PLAIN=value\r\n" +
		"export EXPORTED=yes\n" +
		"  INDENTED = spaced value  \n" +
		"EMPTY=\n" +
		"INLINE=abc # trailing comment\n" +
		"HASH=abc#not-a-comment\n" +
		"URL=postgres://user:p@ss@db:5432/app?sslmode=disable\n" +
		"EQUALS=a=b==\n" +
		"SINGLE='literal $HOME \\n \"double\"' # comment\n" +
		"DOUBLE=\"line1\\nline2\\ttab \\\"quoted\\\" \\$HOME back\\\\slash \\x\"\n" +
		"SPACES=\"  kept  \"\n" +
		"MULTI=\"-----BEGIN KEY-----\nabc\n# not a comment\n-----END KEY-----\"\n" +
		"MULTI_SINGLE='first\nsecond'\n" +
		"  # indented comment\n" +
		"PLAIN=last wins\n" +
		"AFTER=ok"
	pairs, err := ParseDotenv(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []Pair{
		{"PLAIN", "last wins"},
		{"EXPORTED", "yes"},
		{"INDENTED", "spaced value"},
		{"EMPTY", ""},
		{"INLINE", "abc"},
		{"HASH", "abc#not-a-comment"},
		{"URL", "postgres://user:p@ss@db:5432/app?sslmode=disable"},
		{"EQUALS", "a=b=="},
		{"SINGLE", `literal $HOME \n "double"`},
		{"DOUBLE", "line1\nline2\ttab \"quoted\" $HOME back\\slash \\x"},
		{"SPACES", "  kept  "},
		{"MULTI", "-----BEGIN KEY-----\nabc\n# not a comment\n-----END KEY-----"},
		{"MULTI_SINGLE", "first\nsecond"},
		{"AFTER", "ok"},
	}
	if !reflect.DeepEqual(pairs, want) {
		for i := range pairs {
			if i >= len(want) || pairs[i] != want[i] {
				t.Errorf("pair %d: %+v", i, pairs[i])
			}
		}
		t.Fatalf("got %d pairs, want %d", len(pairs), len(want))
	}
	if pairs, err := ParseDotenv("  \n# only comments\n"); err != nil || len(pairs) != 0 {
		t.Fatalf("no variable: %v %v", pairs, err)
	}
}

func TestParseDotenvErrorsNeverQuoteTheText(t *testing.T) {
	const secret = "s3cr3t-in-a-bad-line"
	for name, test := range map[string]struct {
		text string
		line string
	}{
		"no equal sign":         {"A=1\n" + secret + "\n", "ligne 2"},
		"invalid name":          {"A=1\n\nBAD-NAME=" + secret, "ligne 3"},
		"name starting by 1":    {"1A=" + secret, "ligne 1"},
		"empty name":            {"=" + secret, "ligne 1"},
		"unclosed double quote": {"A=1\nB=\"" + secret + "\nC=2\n", "ligne 2"},
		"unclosed single quote": {"A='" + secret, "ligne 1"},
		"text after a quote":    {"A=\"x\"" + secret, "ligne 1"},
		"after a multi-line":    {"A=\"x\ny\"\n" + secret, "ligne 3"},
		"export alone":          {"export " + secret, "ligne 1"},
	} {
		_, err := ParseDotenv(test.text)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), test.line) {
			t.Errorf("%s: %q", name, err.Error())
		}
	}
}

func TestFormatDotenvRoundTrip(t *testing.T) {
	values := []string{
		"", "simple", "with space", " leading and trailing ", "tab\there", "hash # inside", "#leading-hash",
		"single'quote", `double"quote`, `both ' and "`, "dollar $HOME ${X}", `back\slash`, `back\nslash-n`,
		"line1\nline2", "crlf\r\nline", "trailing newline\n", "'", `"`, `\`, "a=b", "é ü 漢字",
		"-----BEGIN KEY-----\nMIIB'x'\"y\"$z\\\n-----END KEY-----\n", "export X=1", "`cmd` $(cmd) ; && |",
	}
	pairs := make([]Pair, len(values))
	for i, value := range values {
		pairs[i] = Pair{Name: "V" + strings.Repeat("X", i), Value: value}
	}
	text := FormatDotenv(pairs)
	back, err := ParseDotenv(text)
	if err != nil {
		t.Fatalf("%v in:\n%s", err, text)
	}
	if !reflect.DeepEqual(back, pairs) {
		for i := range back {
			if i < len(pairs) && back[i] != pairs[i] {
				t.Errorf("%q came back as %q", pairs[i].Value, back[i].Value)
			}
		}
		t.Fatalf("round trip differs:\n%s", text)
	}
	// What is written: bare when harmless, literal single quotes when
	// possible, escaped double quotes otherwise.
	got := FormatDotenv([]Pair{{"A", "abc-1.2/x:y@z"}, {"B", "two words $X"}, {"C", "it's\n"}, {"D", ""}})
	want := "A=abc-1.2/x:y@z\nB='two words $X'\nC=\"it's\\n\"\nD=\n"
	if got != want {
		t.Fatalf("format:\n%s\nwant:\n%s", got, want)
	}
}

func TestImport(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	if _, err := v.SetVar("root", "shop", "KEEP", "kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetVar("root", "shop", "TOKEN", "old"); err != nil {
		t.Fatal(err)
	}
	var validation *ValidationError
	// One bad line stores nothing.
	if _, err := v.Import("root", "shop", "TOKEN=new\nNOT A LINE\n"); !errors.As(err, &validation) {
		t.Fatalf("invalid text: %v", err)
	}
	if _, err := v.Import("root", "shop", "# nothing\n"); !errors.As(err, &validation) {
		t.Fatalf("empty text: %v", err)
	}
	if _, err := v.Import("root", "shop", "A="+strings.Repeat("x", MaxImportBytes)); !errors.As(err, &validation) {
		t.Fatalf("oversized text: %v", err)
	}
	if _, err := v.Import("root", "missing", "A=1"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("missing app: %v", err)
	}
	// The import works while locked, and values are encrypted at once.
	result, err := v.Import("root", "shop", "# prod\nTOKEN=\""+testValue+"\"\nexport DB_URL='postgres://u:p@db/app'\n")
	if err != nil || !reflect.DeepEqual(result.Imported, []string{"TOKEN", "DB_URL"}) || len(result.App.Variables) != 3 {
		t.Fatalf("import: %+v %v", result, err)
	}
	if strings.Contains(string(v.vaultBytes(t)), testValue) {
		t.Fatal("an imported value is stored in clear")
	}
	v.unlock(t)
	exported, err := v.Export("root", "shop")
	if err != nil || exported != "KEEP=kept\nTOKEN="+testValue+"\nDB_URL=postgres://u:p@db/app\n" {
		t.Fatalf("export: %q %v", exported, err)
	}
}
