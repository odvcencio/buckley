package containers

// ComposeFile represents a docker-compose.yml
type ComposeFile struct {
	Version  string             `yaml:"version"`
	Services map[string]Service `yaml:"services"`
	Volumes  map[string]Volume  `yaml:"volumes,omitempty"`
	Networks map[string]Network `yaml:"networks,omitempty"`
}

// Service represents a docker-compose service
type Service struct {
	Image         string            `yaml:"image,omitempty"`
	ContainerName string            `yaml:"container_name,omitempty"`
	Build         *BuildConfig      `yaml:"build,omitempty"`
	WorkingDir    string            `yaml:"working_dir,omitempty"`
	Command       []string          `yaml:"command,omitempty"`
	Entrypoint    []string          `yaml:"entrypoint,omitempty"`
	Ports         []string          `yaml:"ports,omitempty"`
	Volumes       []string          `yaml:"volumes,omitempty"`
	Environment   map[string]string `yaml:"environment,omitempty"`
	EnvFile       []string          `yaml:"env_file,omitempty"`
	DependsOn     []string          `yaml:"depends_on,omitempty"`
	Networks      []string          `yaml:"networks,omitempty"`
	Healthcheck   *Healthcheck      `yaml:"healthcheck,omitempty"`
	Restart       string            `yaml:"restart,omitempty"`
}

// BuildConfig represents build configuration for a service
type BuildConfig struct {
	Context    string            `yaml:"context,omitempty"`
	Dockerfile string            `yaml:"dockerfile,omitempty"`
	Args       map[string]string `yaml:"args,omitempty"`
}

// Healthcheck represents a healthcheck configuration
type Healthcheck struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval,omitempty"`
	Timeout  string   `yaml:"timeout,omitempty"`
	Retries  int      `yaml:"retries,omitempty"`
}

// Volume represents a docker volume
type Volume struct {
	Driver string            `yaml:"driver,omitempty"`
	Labels map[string]string `yaml:"labels,omitempty"`
}

// Network represents a docker network
type Network struct {
	Driver string `yaml:"driver,omitempty"`
}
