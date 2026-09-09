package main

// Usage texts mirrored byte-for-byte from the bash forge usage() and
// command_usage() heredocs. Regenerate by capturing 'forge --help' and
// 'forge <command> --help' from the bash implementation.

const usageText = `Usage: forge [-R|--repo owner/repo] <resource> <command> [args]

Resources:
  pr          Pull requests
  issue       Issues
  label       Repository labels
  milestone   Repository milestones
  project     Repository projects (not exposed by this Forgejo API)
  auth        Credential status
  token       Candidate token verification

PR commands:
  forge pr list
      List open pull requests
  forge pr view <number>
      Show PR details and comments
  forge pr snapshot <number>
      Print a stable immutable PR snapshot as JSON
  forge pr review-comments <number>
      List inline review comments with IDs (for use with reply)
  forge pr reply <number> <comment-id> [-b <text> | -F <file>]
      Reply to an inline review comment thread
  forge pr edit <number> [-t <title>] [-b <text> | -F <file>]
      Update PR title and/or description
  forge pr comment <number> [-b <text> | -F <file>]
      Post a comment
  forge pr create -t <title> [-H <branch>] [-B <branch>] [-b <text> | -F <file>]
      Create a pull request
  forge pr close {<number> | <url> | <branch>} [-c <text>] [-d]
      Close a pull request; -d deletes the remote head branch
  forge pr find-by-head <branch>
      Print PR number if an open PR exists for that head branch (empty if none)

Auth commands:
  forge auth status
      Check the token in the configured token file

Token commands:
  forge token verify
      Verify a candidate token read from stdin

Issue commands:
  forge issue list [-s open|closed|all] [-l <label>] [-m <milestone>] [-L <limit>] [-S <query>]
      List issues
  forge issue view <number>
      Show issue details and comments
  forge issue create -t <title> [-b <text> | -F <file>] [-l <label>] [-m <milestone>]
      Create an issue
  forge issue edit <number> [-t <title>] [-b <text> | -F <file>] [-m <milestone> | --remove-milestone] [--add-label <label>] [--remove-label <label>]
      Update issue title, description, labels, and/or milestone
  forge issue labels <number> [--add-label <label>] [--remove-label <label>] [--set <label>] [--clear]
      List or change issue labels
  forge issue milestone <number> [<milestone> | --clear]
      Show, set, or clear issue milestone
  forge issue comment <number> [-b <text> | -F <file>]
      Post a comment on an issue
  forge issue close {<number> | <url>} [-c <text>] [-r <reason>] [--duplicate-of <issue>]
      Close an issue; reasons: completed, not planned, duplicate

Label commands:
  forge label list [-L <limit>] [-S <query>] [--sort created|name] [--order asc|desc]
      List repository labels
  forge label create <name> [-c <hex>] [-d <text>] [-f] [--exclusive] [--archived]
      Create a repository label
  forge label edit <id-or-name> [-n <name>] [-c <hex>] [-d <text>] [--exclusive | --no-exclusive] [--archived | --no-archived]
      Update a repository label
  forge label delete <name> [--yes]
      Delete a repository label

Milestone commands:
  forge milestone list [-s open|closed|all]
      List repository milestones
  forge milestone view <id-or-title>
      Show milestone details
  forge milestone create -t <title> [-d <text>] [--due <date>] [-s open|closed]
      Create a repository milestone
  forge milestone edit <id-or-title> [-t <title>] [-d <text>] [--due <date>] [-s open|closed]
      Update a repository milestone
  forge milestone delete <id-or-title>
      Delete a repository milestone

Project commands:
  forge project <command>
      Unavailable: this Forgejo API does not expose repository project endpoints

Flags:
  -R, --repo <owner/repo>   Target repo; may appear before resource or after command
  -t, --title <text>        Set a title
  -b, --body <text>         Set body text
  -F, --body-file <file>    Read body text from file (use "-" for stdin)
  -H, --head <branch>       PR head branch (default: current branch)
  -B, --base <branch>       PR base branch (default: repository default)
  -l, --label <label>       Add issue label by name or ID
  -m, --milestone <value>   Set issue milestone by title or ID
  -c, --comment <text>      Leave a closing comment
  -r, --reason <reason>     Closing reason: completed, not planned, duplicate
      --duplicate-of <issue>  Mark the closing comment as a duplicate

Config:
  FORGE_URL          Forgejo base URL (default: https://forge.anarch.diy)
  FORGE_TOKEN_FILE   Path to token file (default: ~/.config/git/forgejo-token)
                     The token is read only from this mode-0600 file; a set
                     FORGEJO_TOKEN is refused, never read
`

var commandUsageTexts = map[string]string{
	"pr list": `Usage: forge pr list [-R <owner/repo>]

List open pull requests.

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr view": `Usage: forge pr view <number> [-R <owner/repo>]

Show PR details and comments.

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr snapshot": `Usage: forge pr snapshot <number> [-R <owner/repo>]

Print a stable, versioned JSON snapshot of a pull request and its immutable
base/head commits. Fork pull requests retain each side's repository identity
and HTTPS clone URL.

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr review-comments": `Usage: forge pr review-comments <number> [-R <owner/repo>]

List inline review comments with IDs (for use with reply).

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr reply": `Usage: forge pr reply <number> <comment-id> [-b <text> | -F <file>] [-R <owner/repo>]

Reply to an inline review comment thread.

Flags:
  -b, --body <text>         Reply body
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr edit": `Usage: forge pr edit <number> [-t <title>] [-b <text> | -F <file>] [-R <owner/repo>]

Update PR title and/or description.

Flags:
  -t, --title <text>        New title
  -b, --body <text>         New body text
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr comment": `Usage: forge pr comment <number> [-b <text> | -F <file>] [-R <owner/repo>]

Post a comment on a pull request.

Flags:
  -b, --body <text>         Comment body
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr create": `Usage: forge pr create -t <title> [-H <branch>] [-B <branch>] [-b <text> | -F <file>] [-R <owner/repo>]

Create a pull request.

Flags:
  -t, --title <text>        PR title (required)
  -H, --head <branch>       Head branch (default: current branch)
  -B, --base <branch>       Base branch (default: repository default)
  -b, --body <text>         Set body text
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr find-by-head": `Usage: forge pr find-by-head <branch> [-R <owner/repo>]

Print PR number if an open PR exists for that head branch (empty if none).

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"pr close": `Usage: forge pr close {<number> | <url> | <branch>} [-c <text>] [-d] [-R <owner/repo>]

Close a pull request.

Flags:
  -c, --comment <text>      Leave a closing comment
  -d, --delete-branch       Delete the remote head branch after closing
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue list": `Usage: forge issue list [-s open|closed|all] [-l <label>] [-m <milestone>] [-L <limit>] [-S <query>] [-R <owner/repo>]

List issues.

Flags:
  -s, --state <state>       Filter by state: open, closed, or all (default: open)
  -l, --label <label>       Filter by label name (repeatable, comma-separated)
  -m, --milestone <value>   Filter by milestone title or ID
  -L, --limit <number>      Maximum number of issues to fetch (default: 30)
  -S, --search <query>      Search issue titles and bodies locally
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue view": `Usage: forge issue view <number> [-R <owner/repo>]

Show issue details and comments.

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue create": `Usage: forge issue create -t <title> [-b <text> | -F <file>] [-l <label>] [-m <milestone>] [-R <owner/repo>]

Create an issue.

Flags:
  -t, --title <text>        Issue title (required)
  -b, --body <text>         Set body text
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -l, --label <label>       Add a label by name or ID (repeatable)
  -m, --milestone <value>   Set milestone by title or ID
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue edit": `Usage: forge issue edit <number> [-t <title>] [-b <text> | -F <file>] [-m <milestone> | --remove-milestone] [--add-label <label>] [--remove-label <label>] [-R <owner/repo>]

Update issue title, description, labels, and/or milestone.

Flags:
  -t, --title <text>        New title
  -b, --body <text>         New body text
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -m, --milestone <value>   Set milestone by title or ID
      --remove-milestone    Remove the issue milestone
      --add-label <label>   Add labels by name or ID (repeatable, comma-separated)
      --remove-label <label>  Remove labels by name or ID (repeatable, comma-separated)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue labels": `Usage: forge issue labels <number> [--add-label <label>] [--remove-label <label>] [--set <label>] [--clear] [-R <owner/repo>]

List or change an issue's labels. Label values may be names or IDs.

Flags:
      --add-label <label>   Add a label (repeatable, comma-separated)
      --remove-label <label>  Remove a label (repeatable, comma-separated)
      --set <label>         Replace labels with this set (repeatable)
      --clear               Remove all labels
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue milestone": `Usage: forge issue milestone <number> [<milestone> | --clear] [-R <owner/repo>]

Show, set, or clear an issue milestone. Milestone values may be titles or IDs.

Flags:
      --clear               Remove the issue milestone
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue comment": `Usage: forge issue comment <number> [-b <text> | -F <file>] [-R <owner/repo>]

Post a comment on an issue.

Flags:
  -b, --body <text>         Comment body
  -F, --body-file <file>    Read body from file (use "-" for stdin)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"issue close": `Usage: forge issue close {<number> | <url>} [-c <text>] [-r <reason>] [--duplicate-of <issue>] [-R <owner/repo>]

Close an issue.

Flags:
  -c, --comment <text>      Leave a closing comment
  -r, --reason <reason>     Closing reason: completed, not planned, duplicate
      --duplicate-of <issue>  Mark as a duplicate of another issue
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"label list": `Usage: forge label list [-L <limit>] [-S <query>] [--sort created|name] [--order asc|desc] [-R <owner/repo>]

List repository labels.

Flags:
  -L, --limit <number>      Maximum number of labels to fetch (default: 30)
  -S, --search <query>      Search label names and descriptions
      --sort <field>        Sort fetched labels: created or name (default: created)
      --order <order>       Order labels: asc or desc (default: asc)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"label create": `Usage: forge label create <name> [-c <hex>] [-d <text>] [-f] [--exclusive] [--archived] [-R <owner/repo>]

Create a repository label.

Flags:
  -c, --color <hex>         Six-digit hex color, with or without # (default: random)
  -d, --description <text>  Label description
  -f, --force               Update color and description if the label exists
      --exclusive           Mark as exclusive
      --archived            Mark as archived
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"label edit": `Usage: forge label edit <id-or-name> [-n <name>] [-c <hex>] [-d <text>] [--exclusive | --no-exclusive] [--archived | --no-archived] [-R <owner/repo>]

Update a repository label.

Flags:
  -n, --name <name>         New label name
  -c, --color <hex>         Six-digit hex color, with or without #
  -d, --description <text>  New label description
      --exclusive           Mark as exclusive
      --no-exclusive        Mark as non-exclusive
      --archived            Mark as archived
      --no-archived         Mark as active
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"label delete": `Usage: forge label delete <name> [--yes] [-R <owner/repo>]

Delete a repository label.

Flags:
      --yes                 Confirm deletion without prompting
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"milestone list": `Usage: forge milestone list [-s open|closed|all] [-R <owner/repo>]

List repository milestones.

Flags:
  -s, --state <state>       Milestone state (default: open)
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"milestone view": `Usage: forge milestone view <id-or-title> [-R <owner/repo>]

Show milestone details.

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"milestone create": `Usage: forge milestone create -t <title> [-d <text>] [--due <date>] [-s open|closed] [-R <owner/repo>]

Create a repository milestone.

Flags:
  -t, --title <title>       Milestone title (required)
  -d, --description <text>  Milestone description
      --due <date>          Due date as YYYY-MM-DD or API date-time
  -s, --state <state>       Milestone state: open or closed
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"milestone edit": `Usage: forge milestone edit <id-or-title> [-t <title>] [-d <text>] [--due <date>] [-s open|closed] [-R <owner/repo>]

Update a repository milestone.

Flags:
  -t, --title <title>       New milestone title
  -d, --description <text>  New milestone description
      --due <date>          Due date as YYYY-MM-DD or API date-time
  -s, --state <state>       Milestone state: open or closed
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"milestone delete": `Usage: forge milestone delete <id-or-title> [-R <owner/repo>]

Delete a repository milestone.

Flags:
  -R, --repo <owner/repo>   Target repository (default: inferred from git remote)
`,
	"project": `Usage: forge project <command>

Project commands are unavailable because this Forgejo API does not expose
repository project endpoints. Use the web UI for projects, or organize issues
through labels and milestones from this CLI.
`,
	"token verify": `Usage: <command> | forge token verify

Verify a candidate token read from stdin against the Forgejo API.
Does not use the configured token — stdin is the only input.

Example:
  extract-token-safely | forge token verify
`,
	"auth status": `Usage: forge auth status

Check whether the token in the configured token file (FORGE_TOKEN_FILE)
is valid. Does not print or expose token material.
`,
}
