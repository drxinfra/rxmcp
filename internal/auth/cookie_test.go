package auth

import "testing"

func TestWithName(t *testing.T) {
	cases := map[string]string{
		"MIIDegQQabc":                    "sungero_client=MIIDegQQabc",
		"MIIDegQQabc==":                  "sungero_client=MIIDegQQabc==",
		"sungero_client=MIIDegQQabc==":   "sungero_client=MIIDegQQabc==",
		"a=1; sungero_client=MIIDegQQ==": "a=1; sungero_client=MIIDegQQ==",
	}
	for in, want := range cases {
		if got := withName(in); got != want {
			t.Errorf("withName(%q) = %q, нужно %q", in, got, want)
		}
	}
}
