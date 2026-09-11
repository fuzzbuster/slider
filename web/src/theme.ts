import { createIcons, LogOut, Moon, Sun } from 'lucide';

type Theme = 'dark' | 'light';

const themeStorageKey = 'theme';

function preferredTheme(): Theme {
  const savedTheme = localStorage.getItem(themeStorageKey);
  if (savedTheme === 'dark' || savedTheme === 'light') {
    return savedTheme;
  }
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

function applyTheme(theme: Theme, toggle: HTMLButtonElement): void {
  document.body.dataset.theme = theme;
  localStorage.setItem(themeStorageKey, theme);

  const nextTheme = theme === 'light' ? 'dark' : 'light';
  toggle.setAttribute('aria-label', `Use ${nextTheme} theme`);
  toggle.title = `Use ${nextTheme} theme`;
  toggle.querySelector<HTMLElement>('[data-theme-icon="light"]')?.toggleAttribute('hidden', theme !== 'light');
  toggle.querySelector<HTMLElement>('[data-theme-icon="dark"]')?.toggleAttribute('hidden', theme !== 'dark');
}

export function initializeTheme(): void {
  createIcons({
    icons: {
      LogOut,
      Moon,
      Sun,
    },
  });

  const toggle = document.querySelector<HTMLButtonElement>('#themeToggle');
  if (!toggle) {
    throw new Error('Theme toggle is missing');
  }

  applyTheme(preferredTheme(), toggle);
  toggle.addEventListener('click', () => {
    applyTheme(document.body.dataset.theme === 'light' ? 'dark' : 'light', toggle);
  });
}
