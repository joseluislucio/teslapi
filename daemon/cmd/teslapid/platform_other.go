//go:build !linux

package main

import "errors"

func newPlatform(opts) (platform, error) {
	return platform{}, errors.New("only runs on Linux; elsewhere use -dev <folder>")
}
