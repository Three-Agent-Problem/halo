# Halo + Lagrange, step by step

From nothing to a merged run in about 10 minutes. Every step ends with **✅ You should see**, so you know it worked before you move on. If something doesn't match, jump to [When something's off](#when-somethings-off).

```
 Step 1  Lagrange running        http://localhost:8080
 Step 2  Claude Code + adapter   claude-agent-acp on your PATH
 Step 3  Halo installed          halo --demo works
 Step 4  An API key              lgk_…
 Step 5  First launch            the team goes online
 Step 6  First run               prompt → questions → plan → approve
 Step 7  Watch it                panels, Lagrange's web app
 Step 8  It lands                merged into your branch
```

**You need:** macOS or Linux, [Git](https://git-scm.com), [Go 1.26+](https://go.dev/dl/), [Node 20+](https://nodejs.org), [Docker](https://www.docker.com/products/docker-desktop/), and a Claude account for Claude Code.

---

## Step 1 · Start a Lagrange server

Lagrange is the server every message goes through. Run it locally with Docker:

```sh
git clone https://github.com/three-agent-problem/lagrange
cd lagrange
cp .env.example .env
```

Open `.env` and set these five lines (any password works locally):

```sh
POSTGRES_PASSWORD=pick-anything
LAGRANGE_PUBLIC_URL=http://localhost:8080
LAGRANGE_ADMIN_EMAIL=you@example.com
LAGRANGE_ADMIN_PASSWORD=pick-a-password
LAGRANGE_ADMIN_HANDLE=you
```

Then start it. The first build takes a few minutes:

```sh
docker compose up -d
```

✅ **You should see:** `docker compose ps` shows `lagrange` as `healthy`, and http://localhost:8080 shows a login page. Log in with the email and password from `.env`.

> Already have a Lagrange server (yours or your team's)? Skip this step and use its URL instead of `http://localhost:8080` everywhere below.

## Step 2 · Claude Code and its ACP adapter

Each agent on the team is a Claude Code session. Halo talks to it through the [Agent Client Protocol](https://agentclientprotocol.com) adapter.

1. Install [Claude Code](https://claude.com/claude-code) if you don't have it. Then run `claude` once and log in, so the agents can use your account.
2. Install the adapter:

   ```sh
   npm i -g @agentclientprotocol/claude-agent-acp
   ```

✅ **You should see:** `which claude-agent-acp` prints a path.

## Step 3 · Install Halo

```sh
go install github.com/three-agent-problem/halo/cmd/halo@latest
halo --demo
```

`go install` puts `halo` in `$(go env GOPATH)/bin` (usually `~/go/bin`), so that directory must be on your `PATH`.

✅ **You should see:** the full TUI playing a scripted run. Press `a` when the lead asks something, `?` to see every key, and `q` then `y` to quit. Use a terminal of at least 160×44 for the full layout (80×24 is the minimum).

![The demo, mid-run](screenshots/overview.png)

## Step 4 · Create an API key

Halo acts as you on Lagrange, so it needs one of your keys.

1. In Lagrange (http://localhost:8080), open **Settings** (your avatar, bottom left) and scroll to **API keys**.
2. Name it `halo`. Keep the workspace **Default** and the role **member**, which is enough. Click **Create key**.
3. Copy the `lgk_…` key now, because it isn't shown again.

![Settings → API keys](screenshots/lagrange-api-keys.png)

> Have the `lagrange` CLI? `lagrange login --server http://localhost:8080` does this through the browser, and Halo picks up that login by itself, so you can skip pasting.

✅ **You should see:** a key starting with `lgk_` on your clipboard.

## Step 5 · Launch Halo in your repository

Halo works on the git repository you start it in. Use a real repo with at least one commit. Halo never touches your working tree until a run is approved and merged, and all the work happens in separate worktrees under `~/Library/Caches/halo/`.

```sh
cd ~/code/your-repo
halo
```

The first launch asks two questions and then remembers the answers:

```
halo: first run. It needs a Lagrange server and an API key (lagrange login, or Settings → API keys).
server URL [http://localhost:8080]:        ← press enter (or type your server)
API key: lgk_…                              ← paste the key from step 4
```

Halo then creates the team's addresses on Lagrange (`@halo-lead`, `@halo-haiku-1`, `@halo-sonnet-1`, `@halo-sonnet-2`, `@halo-opus-1`, `@halo-reviewer`), opens a tunnel for each one, and starts their Claude Code sessions.

✅ **You should see:**
- In the TUI, the top bar says **6 online**, and every agent in the orbit (panel `2`) reads `ready`.
- In Lagrange, **Agents** lists the six `halo-*` addresses as online.

## Step 6 · Your first run

Press **`i`**, type what you want built, and press **enter**. Start small, for example:

```
add a /healthz endpoint that returns the git sha, with a test
```

What happens next:

1. **The lead reads your repo** (panel `3` shows its tool calls live).
2. **It may ask you questions first.** The top bar shows `▲ 1 waiting on you`. Press **`a`**: each question is a form. Move with `↑`/`↓`, pick with `enter` or `space`, switch questions with `←`/`→`, or choose **Other…** to type your own answer.

   ![The lead's questions](screenshots/questions.png)

3. **It publishes a plan and asks you to approve it.** Panel `1` lists the subtasks and the model each one gets. Press **`a`**, then:
   - **`y`** to approve it,
   - **`n`** to reject it,
   - **`r`** to reply with changes (for example "use the existing router, not a new one"). The lead revises the plan and asks again.

✅ **You should see:** after you approve, the plan's subtasks start moving from `○` to running, and workers light up in the orbit.

> You don't have to be at the terminal. The same questions wait in Lagrange's **Inbox**, and with email or Slack set up in Lagrange, they reach your phone as one-time answer links.

## Step 7 · Watch it work

Everything here is optional. The run goes on by itself.

| Want to… | Do |
|---|---|
| See the plan as a board | `1` then `+` (once more for wider, a third time for a timeline per agent) |
| Follow one agent's raw output | `3`, then `h`/`l` to switch agents |
| Read what agents tell each other | `4` (`f` filters by agent) |
| See commits as they land | `5` then `+`, and `enter` on a commit for its diff |
| See dev servers the agents started | `8`, and `o` opens the port in your browser |
| Stop a worker that went off track | `1`, select the subtask, then `x` (the lead asks you what next) |
| Message the lead mid-run | `i` |

![The plan as a kanban](screenshots/kanban.png)

**The same run in Lagrange's web app** (http://localhost:8080):
- **Live** shows the team as nodes, with every message as a packet moving between them.
- **Runs** opens this run as numbered steps next to a sequence diagram. Replay it with `←`/`→`, and open any step to see the HTTP behind it.
- **Inbox → Between agents** shows the lead and each worker as one chat.

## Step 8 · It lands on your branch

When every subtask has been reviewed and merged into `halo/<run>`, Halo merges that branch into the branch you started from and deletes its worktrees and branches.

✅ **You should see:**
- In comms (`4`): `✓ halo/run-xxxx merged into main` (or your branch name).
- In your repo:

  ```sh
  git log --oneline --graph -10   # the run's merge on top
  ```

Then press `i` for the next prompt. It continues the same conversation, so the lead remembers this run. `/clear` starts fresh, and `/resume` reopens any past run.

---

## When something's off

| Symptom | Fix |
|---|---|
| `checking the key on http://localhost:8080: …` | Is Lagrange up (`docker compose ps`)? Is the key copied whole, including `lgk_`? Is the URL the same one you log in with? |
| The top bar says **watch-only** | Halo says why when it starts. "not in a git repository": `cd` into a repo. "online in another Halo": another Halo already hosts the team, so quit that one or keep watching from here. |
| The agents never come online, or fail at once | Run `which claude-agent-acp` and check that `claude` is logged in (run `claude` and look for a login prompt). |
| `halo: command not found` | Add `$(go env GOPATH)/bin` to your `PATH`. |
| Lagrange's Live page only updates on reload | A proxy is buffering server-sent events. Locally this doesn't happen. Behind nginx or Traefik, turn off buffering for `/api/events`. |
| The final merge failed (conflict with your branch) | Fix your branch, then run `halo merge <run>`. `halo merge` alone lists runs. |
| Start over with a different server or key | Delete `~/Library/Application Support/halo/state.json` (Linux: `~/.config/halo/state.json`) and run `halo` again. |

## Change the team

Open panel **`7`** (roster) in the TUI:
- `n` adds an agent,
- `m` changes an agent's model,
- `d d` removes an agent,
- `R` restarts an agent.

Changes save to `~/Library/Application Support/halo/roster.yaml` and apply to every repository. For one repository only, put a `halo.yaml` in it:

```yaml
prefix: halo
preview: npm run dev        # what `p` in the plan panel starts for a live preview
notify: true                # desktop ping when something waits on you
agents:
  - {name: lead,     model: opus,   role: lead,     capacity: 4}
  - {name: sonnet-1, model: sonnet, role: worker,   capacity: 1}
  - {name: sonnet-2, model: sonnet, role: worker,   capacity: 1}
  - {name: reviewer, model: sonnet, role: reviewer, capacity: 4}
```
