package skills

const (
	Builtin = "builtin"
	Shared  = "shared"
)

type Version struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

type Manifest struct {
	Builtin Version   `json:"builtin"`
	Shared  []Version `json:"shared"`
	Removed []Version `json:"removed,omitempty"`
}

type Selector struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	Digest   string `json:"digest,omitempty"`
}
