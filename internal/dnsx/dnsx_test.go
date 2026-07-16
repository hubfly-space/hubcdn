package dnsx

import "testing"

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://origin.example.com", "https://origin.example.com", false},
		{"http://10.0.0.5:8080", "http://10.0.0.5:8080", false},
		{"origin.example.com", "https://origin.example.com", false},
		{" https://origin.example.com/base/ ", "https://origin.example.com/base/", false},
		{"https://user:pass@origin.example.com?q=1#f", "https://origin.example.com", false},
		{"ftp://origin.example.com", "", true},
		{"", "", true},
		{"https://", "", true},
	}
	for _, tt := range tests {
		u, err := ParseOrigin(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseOrigin(%q): want error, got %v", tt.in, u)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOrigin(%q): %v", tt.in, err)
			continue
		}
		if u.String() != tt.want {
			t.Errorf("ParseOrigin(%q) = %q, want %q", tt.in, u, tt.want)
		}
	}
}

func TestApex(t *testing.T) {
	tests := []struct{ in, want string }{
		{"example.com", "example.com"},
		{"www.example.com", "example.com"},
		{"a.b.c.example.co.uk", "example.co.uk"},
		{"Example.COM.", "example.com"},
	}
	for _, tt := range tests {
		if got := Apex(tt.in); got != tt.want {
			t.Errorf("Apex(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidHost(t *testing.T) {
	valid := []string{"example.com", "www.example.com", "a-b.example.co.uk", "xn--nxasmq6b.com"}
	invalid := []string{"", "localhost", "192.168.1.1", "2001:db8::1", "UPPER.example.com",
		"-bad.example.com", "bad-.example.com", "exa mple.com", "example..com"}
	for _, h := range valid {
		if !ValidHost(h) {
			t.Errorf("ValidHost(%q) = false, want true", h)
		}
	}
	for _, h := range invalid {
		if ValidHost(h) {
			t.Errorf("ValidHost(%q) = true, want false", h)
		}
	}
}
