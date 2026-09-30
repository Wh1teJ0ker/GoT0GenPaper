package scan

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
)

type loadedPage struct {
	Path   string
	Image  image.Image
	Width  int
	Height int
}

func loadPage(path string) (loadedPage, error) {
	f, err := os.Open(path)
	if err != nil {
		return loadedPage{}, fmt.Errorf("open scan %s: %w", path, err)
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		return loadedPage{}, fmt.Errorf("decode scan %s: %w", path, err)
	}
	b := im.Bounds()
	return loadedPage{Path: path, Image: im, Width: b.Dx(), Height: b.Dy()}, nil
}
