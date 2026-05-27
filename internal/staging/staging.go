package staging

import (
	"strings"
	"unicode"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/config"
)

func RuntimeEnv(cfg config.StagingConfig, runID string, inputs []app.DataInput) map[string]string {
	env := map[string]string{}
	if cfg.CheckpointURIPrefix != "" {
		prefix := CheckpointPrefix(cfg, runID)
		env["SWITCHBOARD_CHECKPOINT_URI_PREFIX"] = prefix
		env["ORCHESTRATOR_CHECKPOINT_URI_PREFIX"] = prefix
	}
	if cfg.DataURIPrefix != "" {
		prefix := DataPrefix(cfg, runID)
		env["SWITCHBOARD_DATA_URI_PREFIX"] = prefix
		env["ORCHESTRATOR_DATA_URI_PREFIX"] = prefix
	}
	for _, input := range inputs {
		if input.Mode != app.DataInputModeURI || input.Source == "" || input.Name == "" {
			continue
		}
		key := inputEnvKey(input.Name)
		if key == "" {
			continue
		}
		env["SWITCHBOARD_DATA_"+key+"_URI"] = input.Source
		if input.Mount != "" {
			env["SWITCHBOARD_DATA_"+key+"_MOUNT"] = input.Mount
		}
	}
	return env
}

func CheckpointPrefix(cfg config.StagingConfig, runID string) string {
	return joinURI(cfg.CheckpointURIPrefix, runID, "checkpoints")
}

func DataPrefix(cfg config.StagingConfig, runID string) string {
	return joinURI(cfg.DataURIPrefix, runID, "data")
}

func JoinURI(parts ...string) string {
	return joinURI(parts...)
}

func joinURI(parts ...string) string {
	var cleaned []string
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 {
			cleaned = append(cleaned, strings.TrimRight(part, "/"))
			continue
		}
		cleaned = append(cleaned, strings.Trim(part, "/"))
	}
	return strings.Join(cleaned, "/")
}

func inputEnvKey(name string) string {
	var out strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(unicode.ToUpper(r))
			lastUnderscore = false
		default:
			if !lastUnderscore && out.Len() > 0 {
				out.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(out.String(), "_")
}
