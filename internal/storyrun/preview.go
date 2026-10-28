package storyrun

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Screen is one clickable page in the before-merge preview.
type Screen struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Kind  string `json:"kind"`
}

var sentenceSplit = regexp.MustCompile(`[.!?\n]+`)

// Plan is the short path Blink will follow for a Tier 1 story.
func Plan() []string {
	return []string{
		"Check the tier in code. No extra model call.",
		"Show this preview before anything is merged.",
		"Implement the story on its own branch and run tests.",
		"Open a draft pull request. A person merges.",
	}
}

// Screens turns a requirement into a three-step flow the user can click through.
func Screens(title, requirement string) []Screen {
	heading := strings.TrimSpace(title)
	if heading == "" {
		heading = firstLine(requirement)
	}
	if heading == "" {
		heading = "Story"
	}
	behaviors := behaviorsFrom(requirement)
	body := strings.TrimSpace(requirement)
	if body == "" {
		body = "Describe what the user should be able to do."
	}
	return []Screen{
		{ID: "story", Label: "Story", Title: heading, Body: body, Kind: "story"},
		{ID: "build", Label: "What ships", Title: "What this story builds", Body: strings.Join(behaviors, "\n"), Kind: "build"},
		{ID: "review", Label: "Before merge", Title: "Ready for review", Body: "This is the unmerged story. Production does not change until a person merges the pull request.", Kind: "review"},
	}
}

func behaviorsFrom(requirement string) []string {
	var out []string
	for _, part := range sentenceSplit.Split(requirement, -1) {
		line := strings.TrimSpace(part)
		if len(line) < 12 {
			continue
		}
		out = append(out, line)
		if len(out) == 6 {
			break
		}
	}
	if len(out) == 0 {
		out = append(out, "The change described in this story is visible here before merge.")
	}
	return out
}

func firstLine(text string) string {
	line := strings.TrimSpace(strings.Split(text, "\n")[0])
	if len(line) > 80 {
		return line[:77] + "..."
	}
	return line
}

// PreviewHTML is a self-contained page. User text is JSON-escaped, never interpolated raw.
func PreviewHTML(title, requirement string, pullRequests []string) string {
	payload, err := json.Marshal(map[string]any{
		"screens":      Screens(title, requirement),
		"pullRequests": pullRequests,
	})
	if err != nil {
		payload = []byte(`{"screens":[],"pullRequests":[]}`)
	}
	return strings.Replace(previewTemplate, "__DATA__", string(payload), 1)
}

const previewTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Blink preview</title>
<style>
  :root { color-scheme: light; font-family: "Segoe UI", sans-serif; }
  body { margin: 0; background: #f4f1ea; color: #1c1915; }
  .banner { background: #1c1915; color: #f4f1ea; padding: 10px 16px; font-size: 13px; }
  .shell { display: grid; grid-template-columns: 180px 1fr; min-height: calc(100vh - 38px); }
  nav { background: #fff; border-right: 1px solid #e4ddd0; padding: 16px 12px; }
  nav button { display: block; width: 100%; text-align: left; margin: 0 0 8px; padding: 8px 10px; border: 0; border-radius: 8px; background: transparent; cursor: pointer; }
  nav button.active { background: #1c1915; color: #fff; }
  main { padding: 28px; }
  h1 { margin: 0 0 8px; font-size: 28px; }
  p { line-height: 1.5; }
  .lead { color: #5c564c; }
  .card { background: #fff; border: 1px solid #e4ddd0; border-radius: 12px; padding: 16px; margin-top: 16px; }
  label { display: flex; gap: 10px; align-items: flex-start; padding: 8px 0; }
  button.primary { margin-top: 16px; background: #1c1915; color: #fff; border: 0; border-radius: 999px; padding: 10px 16px; cursor: pointer; }
  ul { padding-left: 18px; }
  a { color: #1c1915; }
</style>
</head>
<body>
  <div class="banner">Unmerged preview. Nothing here is on the main branch.</div>
  <div class="shell">
    <nav id="nav"></nav>
    <main id="main"></main>
  </div>
  <script>
    const data = __DATA__;
    let index = 0;
    const done = new Set();
    const nav = document.getElementById('nav');
    const main = document.getElementById('main');
    function render() {
      nav.innerHTML = '';
      data.screens.forEach((screen, i) => {
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = screen.label;
        if (i === index) button.className = 'active';
        button.addEventListener('click', () => { index = i; render(); });
        nav.appendChild(button);
      });
      const screen = data.screens[index];
      main.innerHTML = '';
      const title = document.createElement('h1');
      title.textContent = screen.title;
      main.appendChild(title);
      if (screen.kind === 'build') {
        const note = document.createElement('p');
        note.className = 'lead';
        note.textContent = 'Check each behavior. This is the story you would be accepting.';
        main.appendChild(note);
        const card = document.createElement('div');
        card.className = 'card';
        screen.body.split('\n').filter(Boolean).forEach((line, i) => {
          const label = document.createElement('label');
          const box = document.createElement('input');
          box.type = 'checkbox';
          box.checked = done.has(i);
          box.addEventListener('change', () => {
            if (box.checked) done.add(i); else done.delete(i);
          });
          label.appendChild(box);
          label.appendChild(document.createTextNode(line));
          card.appendChild(label);
        });
        main.appendChild(card);
      } else if (screen.kind === 'review') {
        const note = document.createElement('p');
        note.className = 'lead';
        note.textContent = screen.body;
        main.appendChild(note);
        const card = document.createElement('div');
        card.className = 'card';
        const count = document.createElement('p');
        count.textContent = done.size + ' behavior(s) marked ready.';
        card.appendChild(count);
        if (data.pullRequests && data.pullRequests.length) {
          const list = document.createElement('ul');
          data.pullRequests.forEach((url) => {
            const item = document.createElement('li');
            const link = document.createElement('a');
            link.href = url;
            link.target = '_blank';
            link.rel = 'noreferrer';
            link.textContent = url;
            item.appendChild(link);
            list.appendChild(item);
          });
          card.appendChild(list);
        } else {
          const empty = document.createElement('p');
          empty.textContent = 'No draft pull request yet. The preview still shows the story before merge.';
          card.appendChild(empty);
        }
        main.appendChild(card);
      } else {
        const note = document.createElement('p');
        note.className = 'lead';
        note.textContent = screen.body;
        main.appendChild(note);
      }
      if (index < data.screens.length - 1) {
        const next = document.createElement('button');
        next.className = 'primary';
        next.type = 'button';
        next.textContent = 'Continue';
        next.addEventListener('click', () => { index += 1; render(); });
        main.appendChild(next);
      }
    }
    render();
  </script>
</body>
</html>`
