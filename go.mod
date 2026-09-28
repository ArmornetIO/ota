module github.com/ArmornetIO/ota

go 1.24

require (
	github.com/ArmornetIO/ota/lifecycle v0.0.0
	github.com/ArmornetIO/ota/service v0.0.0
)

// lifecycle and service are their own modules so they can be adopted without
// the updater; inside this repo they resolve from the tree.
replace (
	github.com/ArmornetIO/ota/lifecycle => ./lifecycle
	github.com/ArmornetIO/ota/service => ./service
)
