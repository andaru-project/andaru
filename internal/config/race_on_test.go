//go:build race

package config

// raceSlowdown scales timing limits: the race detector runs code several times slower.
const raceSlowdown = 10
