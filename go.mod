module llvm_lang

go 1.26

require (
	github.com/spf13/afero v1.15.0
	golang.org/x/tools v0.48.0
	gopkg.in/yaml.v3 v3.0.1
	tinygo.org/x/go-llvm v0.0.0-20260721072906-185673ef46a5
)

require (
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.28.0 // indirect
)

replace tinygo.org/x/go-llvm => ./third_party/go-llvm
