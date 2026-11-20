package controllers

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"

	"github.com/go-pdf/fpdf"
)

// safeRegisterImage registers an image with fpdf without letting a bad image
// poison the whole document. fpdf records image parse failures (interlaced or
// 16-bit PNGs, mislabelled formats, corrupt data, ...) as a sticky internal
// error that makes every later call a no-op and Output() fail. On failure we
// clear that error and retry once with the image re-encoded as a plain 8-bit
// PNG via the standard library; if that also fails the image is skipped and
// the PDF is still generated. Returns the registered image info and the name
// to pass to pdf.ImageOptions, or nil when the image could not be used.
func safeRegisterImage(pdf *fpdf.Fpdf, name, imgType string, data []byte) (*fpdf.ImageInfoType, string) {
	if pdf.Err() || len(data) == 0 {
		return nil, ""
	}
	if imgType != "" {
		info := pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: imgType, ReadDpi: true}, bytes.NewReader(data))
		if !pdf.Err() && info != nil {
			return info, name
		}
		if pdf.Err() {
			log.Printf("pdf: image %q (%s, %d bytes) rejected by fpdf: %v; retrying as re-encoded PNG", name, imgType, len(data), pdf.Error())
			pdf.ClearError()
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		log.Printf("pdf: image %q could not be decoded, skipping: %v", name, err)
		return nil, ""
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("pdf: image %q could not be re-encoded, skipping: %v", name, err)
		return nil, ""
	}
	retryName := name + "-png"
	info := pdf.RegisterImageOptionsReader(retryName, fpdf.ImageOptions{ImageType: "PNG", ReadDpi: true}, &buf)
	if pdf.Err() || info == nil {
		log.Printf("pdf: image %q still rejected after re-encode, skipping: %v", name, pdf.Error())
		pdf.ClearError()
		return nil, ""
	}
	return info, retryName
}
