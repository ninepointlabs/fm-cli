package cli

import "git.sr.ht/~rockorager/go-jmap/mail/email"

// emailAlias keeps the go-jmap Email type name out of watch.go's signatures.
type emailAlias = email.Email
