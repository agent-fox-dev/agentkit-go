package sample

// Doc comment for Greet.
func Greet(name string) string {
	return "Hello, " + name
}

type Server struct {
	Addr string
}

// Doc comment for Start.
func (s *Server) Start() error {
	return nil
}

type Handler interface {
	Handle(req string) string
}

type Config map[string]string

type Alias = []byte

const (
	MaxRetries = 3
	minDelay   = 100
)

var (
	DefaultAddr string
	verbose     bool
)
