package serviceurl

import "testing"

func TestValidateAstralURLs(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:4545", "http://192.168.1.20:4545", "http://astral.example.ts.net:4545", "http://[fd7a:115c:a1e0::1]:4545", "https://astral.example/base"} {
		if err := Validate(value); err != nil {
			t.Errorf("rejected %s: %v", value, err)
		}
	}
	for _, value := range []string{"", "http://", "http://:4545", "ftp://server", "//server", "http://user:password@server", "http://server?token=secret", "http://server?", "http://server#part", " http://server", "http://0.0.0.0:4545", "http://[::]:4545", "http://server:0", "http://server:65536", "http://*:4545"} {
		if err := Validate(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
