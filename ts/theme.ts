export function toggleTheme(): void {
    const html = document.documentElement;
    if (html.classList.contains('eink-mode')) return; // e-ink overrides theme
    const isLight = html.classList.toggle('light-theme');
    localStorage.setItem('gitlens-theme', isLight ? 'light' : 'dark');
    updateThemeIcons(isLight);
}

export function initTheme(): void {
    const isLight = document.documentElement.classList.contains('light-theme');
    updateThemeIcons(isLight);
}

function updateThemeIcons(isLight: boolean): void {
    const sun = document.querySelector('.theme-toggle-sun') as HTMLElement | null;
    const moon = document.querySelector('.theme-toggle-moon') as HTMLElement | null;
    if (sun) sun.style.display = isLight ? 'none' : '';
    if (moon) moon.style.display = isLight ? '' : 'none';
}
