package skills

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v3"
)

type SkillSource struct {
	Dir    string
	Source string
}

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

func LoadSkills(sources ...SkillSource) ([]*Skill, error) {
	seen := make(map[string]bool)
	var result []*Skill

	for _, src := range sources {
		if _, err := os.Stat(src.Dir); os.IsNotExist(err) {
			continue
		}
		warnLegacyYAML(src.Dir)

		entries, err := os.ReadDir(src.Dir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)

		for _, name := range names {
			if seen[name] {
				continue
			}
			skillDir := filepath.Join(src.Dir, name)
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); os.IsNotExist(err) {
				continue
			}
			s, err := loadSkillMD(skillFile, name, src.Source)
			if err != nil {
				return nil, fmt.Errorf("loading skill %s: %w", name, err)
			}
			s.Dir = skillDir
			seen[name] = true
			result = append(result, s)
		}
	}
	return result, nil
}

func loadSkillMD(path, dirName, source string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fm skillFrontmatter
	body, err := frontmatter.Parse(bytes.NewReader(data), &fm, frontmatter.NewFormat("---", "---", yaml.Unmarshal))
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("skill name is required in frontmatter")
	}
	if fm.Name != dirName {
		return nil, fmt.Errorf("skill name %q must match directory name %q", fm.Name, dirName)
	}
	if err := ValidateSkillName(fm.Name); err != nil {
		return nil, err
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("skill description is required")
	}
	var allowedTools []string
	if fm.AllowedTools != "" {
		allowedTools = strings.Fields(fm.AllowedTools)
	}
	return &Skill{
		Name:          fm.Name,
		Description:   fm.Description,
		Prompt:        strings.TrimSpace(string(body)),
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Metadata:      fm.Metadata,
		AllowedTools:  allowedTools,
		Source:        source,
	}, nil
}

func warnLegacyYAML(dir string) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if len(matches) > 0 {
		log.Printf("WARNING: Found legacy skills/*.yaml files in %s — migrate to SKILL.md format", dir)
	}
	matches, _ = filepath.Glob(filepath.Join(dir, "*.yml"))
	if len(matches) > 0 {
		log.Printf("WARNING: Found legacy skills/*.yml files in %s — migrate to SKILL.md format", dir)
	}
}
