package httpapi

import "embed"

//go:embed templates/*.html assets/*
var webFiles embed.FS
