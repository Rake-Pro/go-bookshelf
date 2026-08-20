package audio_test

import "os"

func writeFile(path string, body []byte) error { return os.WriteFile(path, body, 0o644) }
