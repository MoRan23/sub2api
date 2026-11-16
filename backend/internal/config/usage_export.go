package config

// UsageExportConfig keeps generated files on a private shared persistent volume.
type UsageExportConfig struct {
	Directory string `mapstructure:"directory"`
	MaxRows   int64  `mapstructure:"max_rows"`
}
