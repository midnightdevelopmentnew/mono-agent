package crawl

import "testing"

func TestValidateFetchURL_BlocksSSRF(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/admin",
		"http://[::1]/",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata endpoint
		"http://10.0.0.1/internal",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
		"http://0.0.0.0/",
		"file:///etc/passwd",     // non-http scheme
		"gopher://127.0.0.1:70/", // non-http scheme
	}
	for _, u := range blocked {
		if err := validateFetchURL(u); err == nil {
			t.Errorf("validateFetchURL(%q) = nil, want error", u)
		}
	}
}

func TestValidateFetchURL_AllowsPublicLiteral(t *testing.T) {
	// A public IP literal needs no DNS and must be permitted.
	if err := validateFetchURL("https://93.184.216.34/"); err != nil {
		t.Errorf("validateFetchURL(public IP) = %v, want nil", err)
	}
}
