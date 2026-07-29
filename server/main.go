// Command server runs the authoritative Asteroid Salvage simulation.
//
//	go run ./           			 single-player-friendly defaults, dev console enabled
//	go run ./ -teams 4 -teamsize 4   the standard 16-player match
//	go run ./ -coop                  everyone on one team, shared score
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gnet "asteroidsalvage/net"
	"asteroidsalvage/sim"
)

// parseMode maps the -mode flag onto a GameMode, defaulting to salvage for anything
// unrecognised rather than refusing to start: a typo should not cost somebody a match.
func parseMode(name string) sim.GameMode {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "hoard", "hungry", "hippo":
		return sim.ModeHoard
	case "koth", "king", "hill", "kingofthehill":
		return sim.ModeKingOfTheHill
	default:
		return sim.ModeSalvage
	}
}

func main() {
	var (
		addr     = flag.String("addr", ":8080", "listen address")
		teams    = flag.Int("teams", 4, "number of teams")
		teamSize = flag.Int("teamsize", 4, "players per team")
		coop     = flag.Int("coop", 0, "1 = co-op: one team, shared score pool")
		seed     = flag.Int64("seed", 0, "world generation seed; 0 picks a new one each run")
		dev      = flag.Bool("dev", true, "enable the /debug endpoint and live retuning")
		feel     = flag.String("feel", "../config/feel.toml", "tuning file to load and save")

		bestOf       = flag.Int("bestof", 5, "rounds in a match; first to a majority wins")
		roundSecs    = flag.Int("round", 180, "seconds per round")
		interSecs    = flag.Int("intermission", 45, "seconds of shop time between rounds")
		warmupSecs   = flag.Int("warmup", 15, "seconds before the first round")
		lives        = flag.Int("lives", sim.DefaultLives, "shared respawns per team per round (6-20)")
		mode         = flag.String("mode", "salvage", "game mode: salvage, hoard or koth")
		hoardTarget  = flag.Float64("hoard-target", 4000, "hoard: score that ends a round early (0 = run the clock)")
		hoardBonus   = flag.Float64("hoard-bonus", 1.6, "hoard: value multiplier on a rock collected by touch")
		kothTarget   = flag.Float64("koth-target", 500, "king of the hill: score that wins a round")
		kothRate     = flag.Float64("koth-rate", 7, "king of the hill: points per second while holding")
		kothShift    = flag.Int("koth-shift", 45, "king of the hill: seconds between hill moves (0 = fixed)")
		kothCredits  = flag.Float64("koth-credits", 20, "king of the hill: credits per second to each holder")
		sandbox      = flag.Bool("sandbox", false, "one endless round, no clock or shop")
		startCredits = flag.Float64("credits", 0, "credits every player starts with (testing)")
		minEra       = flag.Int("era", 0, "floor the asteroid tier gate, so colossal (2) and titan (3) rocks spawn immediately (testing)")
	)
	flag.Parse()

	cfg := sim.DefaultConfig()
	cfg.TeamCount = *teams
	cfg.TeamSize = *teamSize
	cfg.Coop = *coop != 0
	// A fixed default meant every match was played on the same map. Zero now means "roll
	// one", and the choice is logged so a map worth keeping can be played again with
	// -seed. Generation itself stays fully deterministic in the seed.
	cfg.Seed = *seed
	if cfg.Seed == 0 {
		cfg.Seed = time.Now().UnixNano()
	}
	cfg.Match = sim.MatchConfig{
		BestOf:           *bestOf,
		RoundSeconds:     *roundSecs,
		IntermissionSecs: *interSecs,
		WarmupSeconds:    *warmupSecs,
		DisableMatchFlow: *sandbox,
		Lives:            *lives,
		Mode:             parseMode(*mode),
		Modes: sim.ModeConfig{
			HoardTarget:      float32(*hoardTarget),
			HoardPickupBonus: float32(*hoardBonus),
			KothTarget:       float32(*kothTarget),
			KothRate:         float32(*kothRate),
			KothShiftSecs:    *kothShift,
			KothCredits:      float32(*kothCredits),
		},
	}
	cfg.StartingCredits = float32(*startCredits)
	cfg.MinTierEra = *minEra

	srv, err := gnet.NewServer(gnet.Options{
		Sim:      cfg,
		DevMode:  *dev,
		FeelPath: *feel,
	})
	if err != nil {
		log.Fatalf("startup: %v", err)
	}

	if *dev {
		log.Print("DEV MODE: /debug is open and can retune physics remotely — do not expose this publicly")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go srv.Run(ctx)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("map seed %d (replay this map with -seed %d)", cfg.Seed, cfg.Seed)
		log.Printf("listening on %s (ws://localhost%s/ws)", *addr, *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")

	// Persist whatever was tuned this session rather than losing it on Ctrl-C.
	if *dev {
		if err := srv.SaveTuning(); err != nil {
			log.Printf("saving tuning: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
