package config

type FirecrackerConfig struct {
	ChrootBaseDir string `yaml:"chroot_base_dir" json:"chroot_base_dir"`
	Binary        string `yaml:"binary" json:"binary"`
	Jailer        string `yaml:"jailer" json:"jailer"`
	Kernel        string `yaml:"kernel" json:"kernel"`
	StateDir      string `yaml:"state_dir" json:"state_dir"`
	GuestInit     string `yaml:"guest_init" json:"guest_init"`
	DefaultMemory uint64 `yaml:"default_memory" json:"default_memory"`
}
