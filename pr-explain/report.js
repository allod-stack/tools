(() => {
  document.documentElement.dataset.rxJs = "on";
  const reduce = typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function sequence() {
    const plans = Array.from(document.querySelectorAll(".rx-sequence"))
      .filter((root) => root.dataset.rxEnhanced !== "true")
      .map((root) => {
        const list = root.querySelector(":scope > .rx-seq-steps");
        if (!list) throw new Error("sequence steps");
        const steps = Array.from(list.children);
        if (!steps.length || steps.some((step) => !step.classList.contains("rx-seq-step"))) {
          throw new Error("sequence shape");
        }
        const titles = steps.map((step) => step.querySelector(".rx-seq-title"));
        if (titles.some((title) => !title)) throw new Error("sequence title");

        const controls = document.createElement("div");
        controls.className = "rx-seq-controls";
        const previous = document.createElement("button");
        previous.className = "rx-seq-prev";
        previous.type = "button";
        previous.textContent = "Previous";
        const next = document.createElement("button");
        next.className = "rx-seq-next";
        next.type = "button";
        next.textContent = "Next";
        const all = document.createElement("button");
        all.className = "rx-seq-all";
        all.type = "button";
        all.textContent = "Show all";
        const status = document.createElement("p");
        status.className = "rx-seq-status";
        status.setAttribute("aria-live", "polite");
        controls.append(previous, next, all, status);
        return { root, list, steps, titles, controls, previous, next, all, status };
      });

    plans.forEach((plan) => {
      let index = 0;

      function render(mode, focusTitle) {
        plan.root.dataset.rxMode = mode;
        plan.steps.forEach((step, stepIndex) => {
          if (mode === "step" && stepIndex === index) step.dataset.rxCurrent = "true";
          else delete step.dataset.rxCurrent;
        });
        const showingAll = mode === "all";
        plan.previous.disabled = showingAll || index === 0;
        plan.next.disabled = !showingAll && index === plan.steps.length - 1;
        plan.next.textContent = showingAll ? "Start steps" : "Next";
        plan.all.disabled = showingAll;
        plan.status.textContent = showingAll
          ? `All ${plan.steps.length} steps shown`
          : `Step ${index + 1} of ${plan.steps.length}`;
        if (focusTitle && !showingAll) {
          const title = plan.titles[index];
          title.tabIndex = -1;
          title.focus({ preventScroll: true });
          title.scrollIntoView({ block: "nearest", behavior: reduce ? "auto" : "smooth" });
        }
      }

      plan.previous.addEventListener("click", () => {
        if (index > 0) index -= 1;
        render("step", true);
      });
      plan.next.addEventListener("click", () => {
        if (plan.root.dataset.rxMode === "all") index = 0;
        else if (index < plan.steps.length - 1) index += 1;
        render("step", true);
      });
      plan.all.addEventListener("click", () => render("all", false));
      plan.list.before(plan.controls);
      plan.root.dataset.rxEnhanced = "true";
      render(reduce ? "all" : "step", false);
    });
  }

  function quiz() {
    const roots = Array.from(document.querySelectorAll("section"))
      .filter((root) => root.querySelector(":scope > .rx-quiz-item"));
    const plans = roots.map((root) => {
      const items = Array.from(root.querySelectorAll(":scope > .rx-quiz-item"));
      const itemPlans = items.map((item) => {
        const choices = Array.from(item.querySelectorAll(".rx-choice"));
        const result = item.querySelector(".rx-quiz-result");
        if (!choices.length || !result || choices.some((choice) => !choice.querySelector("summary"))) {
          throw new Error("quiz shape");
        }
        return { item, choices, result };
      });
      let score = root.querySelector(":scope > .rx-quiz-score");
      if (!score) {
        score = document.createElement("p");
        score.className = "rx-quiz-score";
        score.setAttribute("aria-live", "polite");
      }
      return { root, itemPlans, score };
    });

    plans.forEach((plan) => {
      function updateScore() {
        const answered = plan.itemPlans.filter(({ item }) => item.dataset.rxAnswered === "true");
        const correct = answered.filter(({ item }) => item.dataset.rxCorrect === "true");
        plan.score.textContent = `${correct.length} correct out of ${answered.length} answered`;
      }

      plan.itemPlans.forEach(({ item, choices, result }) => {
        choices.forEach((choice) => {
          const summary = choice.querySelector("summary");
          summary.addEventListener("click", (event) => {
            if (item.dataset.rxAnswered === "true" && choice.dataset.rxSelected !== "true") {
              event.preventDefault();
            }
          });
          choice.addEventListener("toggle", () => {
            if (!choice.open || item.dataset.rxAnswered === "true") return;
            const correct = choice.dataset.correct === "true";
            item.dataset.rxAnswered = "true";
            item.dataset.rxCorrect = String(correct);
            choice.dataset.rxSelected = "true";
            choices.forEach((entry) => {
              entry.dataset.rxLocked = "true";
              if (entry !== choice) {
                const lockedSummary = entry.querySelector("summary");
                lockedSummary.setAttribute("aria-disabled", "true");
                lockedSummary.tabIndex = -1;
              }
            });
            result.textContent = correct
              ? "Correct. Answer locked; the revealed explanation states the governing rule."
              : "Not quite. Answer locked; the revealed explanation identifies the misconception."
            updateScore();
          });
        });
      });
      if (!plan.score.isConnected) plan.root.append(plan.score);
      updateScore();
    });
  }

  function codewalk() {
    const plans = Array.from(document.querySelectorAll(".rx-codewalk"))
      .filter((root) => root.dataset.rxEnhanced !== "true")
      .map((root) => {
        const notes = Array.from(root.querySelectorAll(".rx-note[data-hl]"));
        const pairs = notes.map((note) => {
          const mark = document.getElementById(note.dataset.hl);
          if (!mark || !mark.classList.contains("rx-hl")) {
            throw new Error("code highlight");
          }
          return { note, mark };
        });
        return { root, pairs };
      });

    plans.forEach(({ root, pairs }) => {
      pairs.forEach(({ note, mark }) => {
        function light(on) {
          if (on) {
            note.dataset.rxLit = "true";
            mark.dataset.rxLit = "true";
          } else {
            delete note.dataset.rxLit;
            delete mark.dataset.rxLit;
          }
        }
        note.addEventListener("mouseenter", () => light(true));
        note.addEventListener("mouseleave", () => light(false));
        note.addEventListener("focusin", () => {
          light(true);
          mark.scrollIntoView({ block: "nearest", behavior: reduce ? "auto" : "smooth" });
        });
        note.addEventListener("focusout", () => light(false));
        mark.tabIndex = 0;
        mark.addEventListener("mouseenter", () => light(true));
        mark.addEventListener("mouseleave", () => light(false));
        mark.addEventListener("focus", () => {
          light(true);
          note.scrollIntoView({ block: "nearest", behavior: reduce ? "auto" : "smooth" });
        });
        mark.addEventListener("blur", () => light(false));
      });
      root.dataset.rxEnhanced = "true";
    });
  }

  function print() {
    if (document.documentElement.dataset.rxPrintEnhanced === "true") return;
    let opened = [];
    window.addEventListener("beforeprint", () => {
      opened = Array.from(document.querySelectorAll("details:not([open])"));
      opened.forEach((detail) => { detail.open = true; });
    });
    window.addEventListener("afterprint", () => {
      opened.forEach((detail) => { detail.open = false; });
      opened = [];
    });
    document.documentElement.dataset.rxPrintEnhanced = "true";
  }

  const enhancers={quiz, sequence, codewalk, print};
  Object.entries(enhancers).forEach(([name, run]) => {
    try {
      run();
    } catch (error) {
      console.warn(`rx ${name}`, error);
    }
  });
})();
