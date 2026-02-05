# FM-Cli

A minimalist, terminal-based user interface (TUI) for Fastmail, built in Go.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.21+-00ADD8.svg)

## Features

### Email
- **Mailbox Navigation**: Browse your folders with unread counts
- **Email Reading**: Plain text and HTML-to-Markdown rendering with clickable links
- **Composition**: Write emails using your preferred `$EDITOR` (Vim, Nano, etc.)
- **Contact Autocomplete**: Type in the To field and get suggestions from your address book
- **Multiple Identities**: Select from your configured Fastmail sending addresses
- **Reply & Forward**: Reply to sender, reply all, or forward with quoted content
- **Draft Management**: Save, edit, and send drafts
- **Email Actions**: Mark read/unread, flag, archive, and delete
- **Inline Images**: View images in terminal (Sixel/Kitty/iTerm2) or open in browser
- **Pagination**: Infinite scroll through large mailboxes

### Calendar
- **Agenda View**: See upcoming events for the next 7 days
- **Event Management**: Create, edit, and delete events
- **CalDAV Integration**: Syncs with Fastmail calendars

### Contacts
- **Address Book**: Browse and search your contacts
- **Contact Management**: Create, edit, and delete contacts
- **CardDAV Integration**: Syncs with Fastmail address books

### Offline Mode
- **Local Storage**: Emails cached in SQLite for offline reading
- **Full Body Caching**: Email bodies pre-fetched for complete offline access
- **Offline Drafts**: Compose emails offline, sync when back online
- **Pending Actions**: Changes queued and synced automatically

### Other Features
- **Secure Auth**: Credentials stored in system keyring
- **OSC 8 Links**: Clickable hyperlinks in supported terminals
- **Detailed Headers**: Toggle expanded email headers
- **Auto-Refresh**: Automatic sync after actions

## Installation

### From Package Manager

#### Arch Linux (AUR)
```bash
yay -S fm-cli
# or
paru -S fm-cli
```

#### Debian/Ubuntu
```bash
# Download the .deb package from releases
sudo dpkg -i fm-cli_0.2.0_amd64.deb
```

#### Fedora/RHEL
```bash
# Download the .rpm package from releases
sudo rpm -i fm-cli-0.2.0-1.x86_64.rpm
```

### From Source

#### Prerequisites
- Go 1.21+
- CGO enabled (for SQLite support)
- A Fastmail account

#### Build
```bash
git clone https://github.com/timappledotcom/fm-cli.git
cd fm-cli
go build -o fm-cli ./cmd/fm-cli
sudo mv fm-cli /usr/local/bin/
```

## Configuration

### Quick Start

1. **Get your Fastmail credentials** (see below)
2. **Run the login wizard**:
   ```bash
   fm-cli login
   ```
3. **Start the app**:
   ```bash
   fm-cli
   ```

### Getting Your Fastmail Credentials

#### API Token (Required - for email)

1. Log in to [Fastmail](https://www.fastmail.com)
2. Go to **Settings** → **Privacy & Security** → **Integrations** → **API Tokens**
3. Click **New API Token**
4. Give it a name (e.g., "fm-cli")
5. Select permissions: **Mail** (read/write) and **Submission**
6. Copy the generated token

#### App Password (Optional - for calendar/contacts)

1. Go to **Settings** → **Privacy & Security** → **Integrations** → **App Passwords**
2. Click **New App Password**
3. Select **Mail, Contacts & Calendars** access
4. Give it a name (e.g., "fm-cli-dav")
5. Copy the generated password

> **Note**: The App Password uses CalDAV/CardDAV protocols which require separate authentication from the JMAP API token.

### Environment Variables (Alternative)

Instead of using the login command, you can set environment variables:

```bash
export FM_API_TOKEN="your-api-token"
export FM_EMAIL="you@fastmail.com"
export FM_APP_PASSWORD="your-app-password"  # Optional
```

### Data Storage

- **Credentials**: Stored securely in your system keyring
- **Offline data**: `~/.config/fm-cli/emails.db` (SQLite)

## Usage

### Commands

| Command | Description |
| --- | --- |
| `fm-cli` | Start the TUI |
| `fm-cli login` | Store credentials in system keychain |
| `fm-cli logout` | Remove credentials from keychain |
| `fm-cli settings` | View current settings |
| `fm-cli settings offline on` | Enable offline mode |
| `fm-cli settings offline off` | Disable offline mode |
| `fm-cli sync` | Sync pending offline changes |
| `fm-cli debug` | Show debug info (JMAP session, CalDAV/CardDAV status) |
| `fm-cli help` | Show help |

### Offline Mode

Enable offline mode to cache emails locally:

```bash
fm-cli settings offline on
```

When enabled:
- Emails and mailboxes are cached in SQLite
- Email bodies are pre-fetched for complete offline reading
- Read cached emails without internet
- Compose drafts offline (queued for sync)
- Run `fm-cli sync` to push pending changes

### Inline Images

When viewing an email with images:
- Press `b` to open the email in your browser
- Press `i` to render images inline (requires Sixel/Kitty/iTerm2 terminal support)

Supported terminals for inline images:
- Kitty
- iTerm2
- WezTerm
- Foot
- mlterm
- Any terminal with Sixel support

### Controls

#### Global Navigation
| Key | Action |
| --- | --- |
| `0` | Return to main menu |
| `1` | Go to Mail |
| `2` | Go to Calendar |
| `3` | Go to Contacts |
| `4` | Go to Settings |
| `q` | Quit (from main menu) |

#### Main Menu
| Key | Action |
| --- | --- |
| `j` / `k` (or Arrows) | Navigate up/down |
| `Enter` / `l` | Select item |
| `m` | Go to Mail |
| `c` | Go to Calendar |
| `o` | Go to Contacts |
| `s` | Go to Settings |

#### Mailbox List
| Key | Action |
| --- | --- |
| `j` / `k` (or Arrows) | Navigate up/down |
| `Enter` / `l` | Open mailbox |
| `h` / `Esc` | Back to main menu |
| `r` | Refresh |
| `c` | Compose new email |

#### Email List
| Key | Action |
| --- | --- |
| `j` / `k` (or Arrows) | Navigate up/down |
| `Enter` / `l` | Open email |
| `h` / `Esc` | Go back to mailboxes |
| `u` | Toggle read/unread |
| `f` | Toggle flagged |
| `e` | Archive |
| `d` / `Backspace` | Delete |
| `r` | Refresh |
| `c` | Compose new email |

#### Email View
| Key | Action |
| --- | --- |
| `h` / `Esc` | Go back to email list |
| `R` | Reply to sender |
| `A` | Reply all |
| `F` | Forward |
| `m` | Toggle detailed headers |
| `b` | Open in browser |
| `i` | View inline images (if terminal supports) |
| `e` | Edit (drafts only) |

#### Compose
| Key | Action |
| --- | --- |
| `↑` / `↓` | Navigate contact suggestions |
| `Tab` | Select suggestion / Cycle identities |
| `Enter` | Select suggestion / Continue to next field |
| `Esc` | Dismiss suggestions / Cancel |

#### Send Confirmation
| Key | Action |
| --- | --- |
| `y` | Send email |
| `s` | Save as draft |
| `e` | Edit body |
| `n` | Cancel |
| `Tab` | Change sending identity |

#### Calendar (Agenda View)
| Key | Action |
| --- | --- |
| `j` / `k` (or Arrows) | Navigate events |
| `Enter` / `l` | View event details |
| `n` | Create new event |
| `e` | Edit event (from details view) |
| `d` | Delete event |
| `r` | Refresh |
| `h` / `Esc` | Back (from details) or to menu |

#### Calendar Event Editor
| Key | Action |
| --- | --- |
| `Enter` | Save event |
| `Esc` | Cancel |

#### Contacts
| Key | Action |
| --- | --- |
| `j` / `k` (or Arrows) | Navigate contacts |
| `Enter` / `l` | View contact details |
| `n` | Create new contact |
| `e` | Edit contact (from details view) |
| `d` | Delete contact |
| `r` | Refresh |
| `h` / `Esc` | Back (from details) or to menu |

#### Contact Editor
| Key | Action |
| --- | --- |
| `Tab` | Move to next field |
| `Enter` | Save contact |
| `Esc` | Cancel |

#### Settings
| Key | Action |
| --- | --- |
| `j` / `k` (or Arrows) | Navigate |
| `Enter` | Toggle setting |
| `h` / `Esc` / `0` | Back to main menu |

## Troubleshooting

### "No calendars found" or "No address books found"
- Make sure you've configured an App Password (not just the API Token)
- Run `fm-cli debug` to check CalDAV/CardDAV connection status
- The App Password must have "Mail, Contacts & Calendars" permission

### "Calendar/Contacts not available in offline mode"
- Calendar and Contacts require an internet connection
- Only email supports offline mode currently

### Crashes when switching modes
- If you started in offline mode but want to go online, restart the app
- The JMAP client is only initialized at startup

### Images not displaying inline
- Your terminal must support Sixel, Kitty graphics, or iTerm2 inline images
- Use `b` to open in browser as a fallback

## Building Packages

### Debian/Ubuntu (.deb)
```bash
./scripts/build-deb.sh
```

### Fedora/RHEL (.rpm)
```bash
./scripts/build-rpm.sh
```

### Arch Linux (PKGBUILD)
```bash
cd packaging/archlinux
makepkg -si
```

## License

MIT License - see [LICENSE](LICENSE) for details.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.
