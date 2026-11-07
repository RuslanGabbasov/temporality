package presentation

import "os"

func debugRenderOff() bool {
	return os.Getenv("PRESENTATION_DEBUG") != "1"
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
