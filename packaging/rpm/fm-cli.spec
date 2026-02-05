Name:           fm-cli
Version:        0.2.4
Release:        1%{?dist}
Summary:        Terminal-based TUI for Fastmail

License:        MIT
URL:            https://github.com/timappledotcom/fm-cli
Source0:        %{name}-%{version}.tar.gz

%global debug_package %{nil}

BuildRequires:  golang >= 1.21
Requires:       glibc
Recommends:     libsecret

%description
FM-Cli is a minimalist, terminal-based user interface for Fastmail.
Features include email reading/composing, calendar management,
contacts, offline mode, and secure credential storage.

%prep
%setup -q

%build
export CGO_ENABLED=0
go build -ldflags "-s -w -X main.version=%{version}" -o %{name} ./cmd/fm-cli

%install
install -Dm755 %{name} %{buildroot}%{_bindir}/%{name}

%files
%{_bindir}/%{name}
%doc README.md
%license LICENSE

%changelog
* Thu Feb 05 2026 Tim Apple <tim@example.com> - 0.2.4-1
- Remove external SQLite dependency
- Fix build with CGO_ENABLED=0

* Fri Jan 17 2026 Tim Apple <tim@example.com> - 0.2.0-1
- Add inline image support (Sixel/Kitty/iTerm2)
- Add contact autocomplete in compose
- Fix CalDAV/CardDAV endpoint discovery
- Fix offline mode crashes
- Improve contacts navigation and display
- Add full email body caching for offline mode
