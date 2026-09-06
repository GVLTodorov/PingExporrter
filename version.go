package main

// version is overridden at build time via -ldflags "-X main.version=1.2.3"
// and reported as the ping_up{version} label.
var version = "dev"
