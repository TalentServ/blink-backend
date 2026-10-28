package prapp

import "testing"

func TestChooseScreenPrefersCalculatorApp(t *testing.T) {
	files := map[string]string{
		"src/components/HistoryPanel.tsx": "export function HistoryPanel() { return null }\n",
		"src/App.tsx":                     "export default function App() { return <div className=\"keypad\"><button className=\"equals-button\">=</button></div> }\n",
		"other/App.tsx":                   "export default function App() { return <p/> }\n",
	}
	got, ok := ChooseScreen(files)
	if !ok {
		t.Fatal("expected a screen")
	}
	if got.File != "src/App.tsx" || got.Kind != "default" || got.Name != "App" {
		t.Fatalf("got %+v", got)
	}
}

func TestRankPrefersFrontendKeypad(t *testing.T) {
	front := Rank("calculator-frontend", "keypad equals-button evaluateExpression")
	back := Rank("calculator-backend", "evaluateExpression")
	if front <= back {
		t.Fatalf("frontend %d should outrank backend %d", front, back)
	}
}

func TestChooseScreenHTML(t *testing.T) {
	files := map[string]string{
		"public/index.html": "<!doctype html><html><body>Hi</body></html>",
	}
	got, ok := ChooseScreen(files)
	if !ok || got.Kind != "html" || got.Name != "index" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}
