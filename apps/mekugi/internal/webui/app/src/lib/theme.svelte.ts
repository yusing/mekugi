// The page's appearance. System follows the operating system as it changes;
// Light and Dark override it. The choice belongs to this browser and origin,
// not to any machine. The root's data-theme always holds the effective
// appearance, which the palette and code highlighting read.

export const themes = ["system", "light", "dark"] as const;
export type Theme = (typeof themes)[number];

const storageKey = "mekugi.theme";
const dark = matchMedia("(prefers-color-scheme: dark)");

class Appearance {
  choice = $state<Theme>("system");
  storageError = $state<string>();

  constructor() {
    try {
      const saved = localStorage.getItem(storageKey);
      if (themes.includes(saved as Theme)) this.choice = saved as Theme;
    } catch {
      // Unreadable storage leaves System; saving a choice explains the failure.
    }
    dark.addEventListener("change", () => this.apply());
  }

  set(choice: Theme): void {
    this.choice = choice;
    this.apply();
    try {
      localStorage.setItem(storageKey, choice);
      this.storageError = undefined;
    } catch {
      this.storageError =
        "This theme could not be saved and applies until reload. Check this site's browser storage settings.";
    }
  }

  apply(): void {
    document.documentElement.dataset.theme =
      this.choice === "system" ? (dark.matches ? "dark" : "light") : this.choice;
  }
}

export const appearance = new Appearance();
