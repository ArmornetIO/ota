module github.com/ArmornetIO/ota/server/source/github

go 1.27.4

require (
	github.com/ArmornetIO/ota v0.0.0
	github.com/google/go-github/v93 v93.0.0
)

require github.com/google/go-querystring v1.2.0 // indirect

replace (
	github.com/ArmornetIO/ota => ../../..
	github.com/ArmornetIO/ota/lifecycle => ../../../lifecycle
	github.com/ArmornetIO/ota/service => ../../../service
)
