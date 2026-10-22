package main

import (
	"fmt"
	"math/rand"
	"strings"
)

// Operations Team 7 flavour: bot personas and feed lines. All in good fun.

type Kind int

const (
	KindHuman Kind = iota
	KindBot
	KindHarasmus // hunts the nearest human
	KindMitdk    // shsh's baby: slow, innocent, often napping
	KindAIBro    // won't stop talking about AI, to Kafe's despair
)

// botRoster is the order bots join a game: HARASMUS always shows up first.
var botRoster = []struct {
	Name string
	Kind Kind
}{
	{"HARASMUS", KindHarasmus},
	{"mitdk", KindMitdk},
	{"bot-gpt", KindAIBro},
	{"bot-copilot", KindAIBro},
	{"bot-claude", KindAIBro},
}

func pick(lines ...string) string { return lines[rand.Intn(len(lines))] }

func isName(p *Player, name string) bool { return p != nil && strings.EqualFold(p.Name, name) }

// deathLine describes a crash. killer is nil for walls and your own trail.
func deathLine(dead, killer *Player) string {
	d := dead.Name
	switch {
	case dead.Kind == KindMitdk && killer != nil:
		return pick(
			fmt.Sprintf("%s made mitdk cry. shsh is NOT happy", killer.Name),
			fmt.Sprintf("%s bullied a baby. mitdk needs a nap", killer.Name),
		)
	case dead.Kind == KindMitdk:
		return pick("mitdk needs a nap", "mitdk wandered off. shsh, come get your kid")
	case dead.Kind == KindAIBro && killer != nil && isName(killer, "kafe"):
		return fmt.Sprintf("kafe finally made %s shut up about AI", d)
	case dead.Kind == KindAIBro:
		return pick(
			fmt.Sprintf("%s crashed. Kafe: finally, some peace and quiet", d),
			fmt.Sprintf("%s hallucinated a gap in the wall", d),
			fmt.Sprintf("%s ran out of context window", d),
		)
	case killer != nil && killer.Kind == KindHarasmus:
		return pick(
			fmt.Sprintf("%s got harassed by HARASMUS", d),
			fmt.Sprintf("HARASMUS harassed %s. Again.", d),
		)
	case killer != nil && killer.Kind == KindMitdk:
		return fmt.Sprintf("%s lost to a literal baby (mitdk)", d)
	case killer != nil:
		return pick(
			fmt.Sprintf("%s ⟶ %s's trail. FAHHH", d, killer.Name),
			fmt.Sprintf("%s closed %s's case", killer.Name, d),
			fmt.Sprintf("%s escalated %s to the void", killer.Name, d),
			fmt.Sprintf("%s ⟶ %s's trail", d, killer.Name),
		)
	}
	return pick(
		fmt.Sprintf("%s hit the wall. FAHHH", d),
		fmt.Sprintf("FAHHH (%s)", d),
		fmt.Sprintf("%s tripped over their own trail. FAHHH", d),
	)
}

var aiChatter = []string{
	"have you tried asking an LLM?",
	"this round could have been a prompt",
	"I autocompleted your crash",
	"AGI by next round, trust me",
	"great question! turning left",
	"we should add AI to the case queue",
	"I'm not crashing, I'm exploring the latent space",
	"let me just fine-tune my turning",
	"actually, as a large language model…",
}

func chatterLine(p *Player) string {
	return fmt.Sprintf("%s: %s", p.Name, pick(aiChatter...))
}

func caseAssignedLine(p *Player) string {
	return fmt.Sprintf("New case assigned to %s. %s: WHY DID YOU ASSIGN ME THIS CASE", p.Name, p.Name)
}

func caseMovedLine(from, to *Player) string {
	if to.Kind == KindHarasmus {
		return fmt.Sprintf("HARASMUS took %s's case. He did not ask for it", from.Name)
	}
	return pick(
		fmt.Sprintf("%s's case got reassigned to %s", from.Name, to.Name),
		fmt.Sprintf("%s: WHY DID YOU ASSIGN ME THIS CASE (%s's old one)", to.Name, from.Name),
	)
}

func roundStartLines(g *Game) []string {
	var out []string
	var shsh, mitdk, harasmus, kafe bool
	for _, p := range g.players {
		switch {
		case p.Kind == KindMitdk:
			mitdk = true
		case p.Kind == KindHarasmus:
			harasmus = true
		case isName(p, "shsh"):
			shsh = true
		case isName(p, "kafe"):
			kafe = true
		}
	}
	if shsh && mitdk && rand.Intn(3) == 0 {
		out = append(out, "shsh brought mitdk to work again")
	}
	if kafe && rand.Intn(3) == 0 {
		out = append(out, "kafe has asked everyone to please stop talking about AI")
	}
	if harasmus && g.round == 1 {
		out = append(out, "HARASMUS has entered the chat")
	}
	return out
}

func kySpawnLine() string {
	return pick(
		"A KY toolkit appeared. Do not open it.",
		"Someone left KY toolkit lying around",
		"KY toolkit spawned. It's still loading.",
	)
}

func kyOpenLine(p *Player) string {
	return pick(
		fmt.Sprintf("%s opened KY toolkit for everyone. Loading… still loading…", p.Name),
		fmt.Sprintf("%s made everyone log into KY toolkit. Good luck steering", p.Name),
		fmt.Sprintf("%s grabbed KY toolkit. Everyone else: 3%%…", p.Name),
	)
}

// kyProgress is the loading bar of a site that never finishes loading.
func kyProgress(lagLeft int) int {
	steps := []int{1, 1, 2, 3, 3, 3, 3, 7, 12, 12, 12, 13, 13, 13, 99, 99}
	i := (kyLagLen - lagLeft) * len(steps) / kyLagLen
	return steps[min(max(i, 0), len(steps)-1)]
}
