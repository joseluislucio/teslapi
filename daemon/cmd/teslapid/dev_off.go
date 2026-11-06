//go:build !dev

package main

import "errors"

func devPlatform(*opts) (platform, error) {
	return platform{}, errors.New("this build has no development mode (build with -tags dev)")
}
