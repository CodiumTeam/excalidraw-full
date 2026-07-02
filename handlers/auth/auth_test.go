package auth

import "testing"

func TestIsLoginAllowed(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		login string
		want  bool
	}{
		{"empty env allows all", "", "anyone", true},
		{"listed login allowed", "luisrovirosa", "luisrovirosa", true},
		{"unlisted login denied", "luisrovirosa", "someoneelse", false},
		{"case-insensitive", "LuisRovirosa", "luisrovirosa", true},
		{"spaces around commas", " luisrovirosa , bob ", "bob", true},
		{"multi list denies outsider", "a,b,c", "d", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ALLOWED_CREATORS", tc.env)
			if got := isLoginAllowed(tc.login); got != tc.want {
				t.Fatalf("isLoginAllowed(%q) env=%q = %v, want %v", tc.login, tc.env, got, tc.want)
			}
		})
	}
}
