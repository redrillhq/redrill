package config

// Source is a place backups live plus the driver that reads it. Secret-bearing
// fields exist only as *_file / *_env references; there is deliberately no
// inline form (an inline key is rejected by strict parsing).
type Source struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // borg | dumpdir | restic

	// borg / restic
	Repo   string `yaml:"repo"`
	Binary string `yaml:"binary"` // optional override / version pin

	// borg
	PassphraseFile string `yaml:"passphrase_file"`
	PassphraseEnv  string `yaml:"passphrase_env"`
	SSHKeyFile     string `yaml:"ssh_key_file"`

	// restic
	PasswordFile string `yaml:"password_file"`
	PasswordEnv  string `yaml:"password_env"`
	EnvFile      string `yaml:"env_file"`

	// dumpdir
	Path    string `yaml:"path"`
	Pattern string `yaml:"pattern"`
	Pick    string `yaml:"pick"` // newest | all-matching-window
}

func (s *Source) validate(path string, es *errset) {
	switch s.Type {
	case "borg":
		if s.Repo == "" {
			es.add(path+".repo", "required for borg source")
		}
		reject(es, path, "borg", map[string]bool{
			"path": s.Path != "", "pattern": s.Pattern != "", "pick": s.Pick != "",
			"password_file": s.PasswordFile != "", "password_env": s.PasswordEnv != "",
			"env_file": s.EnvFile != "",
		})
	case "restic":
		if s.Repo == "" {
			es.add(path+".repo", "required for restic source")
		}
		if s.PasswordFile == "" && s.PasswordEnv == "" {
			es.add(path, "restic source requires password_file or password_env")
		}
		reject(es, path, "restic", map[string]bool{
			"path": s.Path != "", "pattern": s.Pattern != "", "pick": s.Pick != "",
			"passphrase_file": s.PassphraseFile != "", "passphrase_env": s.PassphraseEnv != "",
			"ssh_key_file": s.SSHKeyFile != "",
		})
	case "dumpdir":
		if s.Path == "" {
			es.add(path+".path", "required for dumpdir source")
		}
		if s.Pattern == "" {
			es.add(path+".pattern", "required for dumpdir source")
		}
		if s.Pick != "" && s.Pick != "newest" && s.Pick != "all-matching-window" {
			es.add(path+".pick", "must be newest or all-matching-window, got %q", s.Pick)
		}
		reject(es, path, "dumpdir", map[string]bool{
			"repo": s.Repo != "", "binary": s.Binary != "",
			"passphrase_file": s.PassphraseFile != "", "passphrase_env": s.PassphraseEnv != "",
			"password_file": s.PasswordFile != "", "password_env": s.PasswordEnv != "",
			"ssh_key_file": s.SSHKeyFile != "", "env_file": s.EnvFile != "",
		})
	case "":
		es.add(path+".type", "required")
	default:
		es.add(path+".type", "unknown source type %q (want borg, dumpdir, restic)", s.Type)
	}
}

func reject(es *errset, path, srcType string, bad map[string]bool) {
	for name, set := range bad {
		if set {
			es.add(path+"."+name, "not valid for %s source", srcType)
		}
	}
}
