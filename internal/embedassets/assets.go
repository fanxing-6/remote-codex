package embedassets

import "embed"

//go:embed dist
var Files embed.FS

func Read(name string) []byte {
	data, err := Files.ReadFile("dist/" + name)
	if err != nil {
		panic(err)
	}
	return data
}
