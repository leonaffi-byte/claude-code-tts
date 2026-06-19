package relay

import (
	"fmt"
	"io"

	qrcode "github.com/skip2/go-qrcode"
)

// PrintQR writes the tokenized companion URL and an ASCII QR code to w.
// baseURL is the scheme+host+port (e.g. "http://192.168.1.5:8766").
func PrintQR(w io.Writer, baseURL, token string) error {
	url := fmt.Sprintf("%s/%s/", baseURL, token)
	fmt.Fprintf(w, "\nCompanion URL: %s\n\n", url) //nolint:errcheck
	qr, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		return fmt.Errorf("qr generation failed: %w", err)
	}
	fmt.Fprintln(w, qr.ToSmallString(false)) //nolint:errcheck
	return nil
}
