package data

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

var supportedURISchemes = map[string]bool{
	"http":  true,
	"https": true,
	"s3":    true,
	"gs":    true,
}

type PreflightOptions struct {
	BundleSizeLimitBytes int64
	RequireOverride      bool
	AllowLargeBundle     bool
}

func Prepare(job app.JobSpec, opts PreflightOptions) (app.DataManifest, error) {
	inputs := make([]app.DataInput, 0, len(job.Data))
	var total int64
	for _, input := range job.Data {
		normalized, err := normalizeInput(input)
		if err != nil {
			return app.DataManifest{}, err
		}
		inputs = append(inputs, normalized)

		switch normalized.Mode {
		case app.DataInputModeBundle:
			size, err := localSize(normalized.Source)
			if err != nil {
				return app.DataManifest{}, err
			}
			total += size
		case app.DataInputModeURI:
			if err := validateURI(normalized.Source); err != nil {
				return app.DataManifest{}, err
			}
		default:
			return app.DataManifest{}, fmt.Errorf("unsupported data input mode %q for %q", normalized.Mode, normalized.Name)
		}
	}

	requiresOverride := opts.RequireOverride && opts.BundleSizeLimitBytes > 0 && total > opts.BundleSizeLimitBytes
	if requiresOverride && !opts.AllowLargeBundle {
		return app.DataManifest{}, fmt.Errorf("data bundle is %d bytes, above limit %d bytes; use URI/object storage or pass --allow-large-data-bundle", total, opts.BundleSizeLimitBytes)
	}

	return app.DataManifest{
		Inputs:                       inputs,
		BundleSizeBytes:              total,
		RequiresLargeBundleOverride:  requiresOverride,
		BundleSizeLimitBytes:         opts.BundleSizeLimitBytes,
		LargeBundleOverridePermitted: opts.AllowLargeBundle,
	}, nil
}

func normalizeInput(input app.DataInput) (app.DataInput, error) {
	if input.Name == "" {
		return app.DataInput{}, fmt.Errorf("data input name is required")
	}
	if input.Source == "" {
		return app.DataInput{}, fmt.Errorf("data input %q source is required", input.Name)
	}
	if input.Mode == "" {
		if isURI(input.Source) {
			input.Mode = app.DataInputModeURI
		} else {
			input.Mode = app.DataInputModeBundle
		}
	}
	if input.Mount == "" {
		input.Mount = "/workspace/data/" + input.Name
	}
	return input, nil
}

func isURI(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && parsed.Scheme != "" && supportedURISchemes[strings.ToLower(parsed.Scheme)]
}

func validateURI(source string) error {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("invalid data URI %q", source)
	}
	if !supportedURISchemes[strings.ToLower(parsed.Scheme)] {
		return fmt.Errorf("unsupported data URI scheme %q", parsed.Scheme)
	}
	return nil
}

func localSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("data input path %q does not exist", path)
		}
		return 0, fmt.Errorf("stat data input path %q: %w", path, err)
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	var total int64
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk data input path %q: %w", path, err)
	}
	return total, nil
}
