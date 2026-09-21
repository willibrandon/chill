package radio

import "testing"

// TestCountryFromLocale covers common POSIX locale forms.
func TestCountryFromLocale(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"en_US.UTF-8", "US"}, {"en-GB", "GB"}, {"nb_NO", "NO"}, {"C", ""}, {"en", ""}, {"de_DE@euro", "DE"},
	} {
		if got := countryFromLocale(tc.in); got != tc.want {
			t.Errorf("countryFromLocale(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
