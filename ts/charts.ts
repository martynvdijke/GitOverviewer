interface ChartData {
    metrics: {
        featPct: number;
        fixPct: number;
        docsPct: number;
        chorePct: number;
        workflowPassRate: number;
        workflowSuccesses: number;
        workflowFailures: number;
        totalRepos: number;
        totalReleases: number;
        totalCommits: number;
        avgLeadTimeHours: number;
    };
}

interface ChartInstances {
    [key: string]: any;
}

const chartInstances: ChartInstances = {};

function destroyCharts(): void {
    Object.values(chartInstances).forEach(c => {
        try { c.destroy(); } catch (_e) { /* ignore */ }
    });
    for (const key in chartInstances) {
        delete chartInstances[key];
    }
}

export function loadChartData(since: string): void {
    const url = since ? '/charts/data?since=' + since : '/charts/data';

    fetch(url)
        .then(r => r.json())
        .then((data: ChartData) => {
            destroyCharts();

            const commitCtx = document.getElementById('commitChart') as HTMLCanvasElement | null;
            if (commitCtx) {
                chartInstances.commit = new (window as any).Chart(commitCtx, {
                    type: 'bar',
                    data: {
                        labels: ['Features', 'Fixes', 'Docs', 'Chore', 'Other'],
                        datasets: [{
                            data: [
                                data.metrics.featPct,
                                data.metrics.fixPct,
                                data.metrics.docsPct,
                                data.metrics.chorePct,
                                Math.max(0, 100 - data.metrics.featPct - data.metrics.fixPct - data.metrics.docsPct - data.metrics.chorePct),
                            ],
                            backgroundColor: ['#3fb950', '#f85149', '#d29922', '#8b949e', '#6e7681'],
                        }],
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: true,
                        plugins: {
                            legend: { display: false },
                            title: { display: true, text: 'Commit Type Distribution', color: '#f0f6fc' },
                        },
                        scales: {
                            y: { beginAtZero: true, max: 100, grid: { color: '#21262d' }, ticks: { color: '#8b949e' } },
                            x: { grid: { display: false }, ticks: { color: '#8b949e' } },
                        },
                    },
                });
            }

            const workflowCtx = document.getElementById('workflowChart') as HTMLCanvasElement | null;
            if (workflowCtx) {
                chartInstances.workflow = new (window as any).Chart(workflowCtx, {
                    type: 'doughnut',
                    data: {
                        labels: [
                            'Passed (' + data.metrics.workflowPassRate + '%)',
                            'Failed (' + (100 - data.metrics.workflowPassRate).toFixed(1) + '%)',
                        ],
                        datasets: [{
                            data: [data.metrics.workflowSuccesses, data.metrics.workflowFailures],
                            backgroundColor: ['#3fb950', '#f85149'],
                        }],
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: true,
                        plugins: {
                            title: { display: true, text: 'Workflow Pass Rate', color: '#f0f6fc' },
                            legend: { labels: { color: '#8b949e' } },
                        },
                    },
                });
            }

            const summaryCtx = document.getElementById('summaryChart') as HTMLCanvasElement | null;
            if (summaryCtx) {
                chartInstances.summary = new (window as any).Chart(summaryCtx, {
                    type: 'bar',
                    data: {
                        labels: ['Repos', 'Releases', 'Commits', 'Lead Time (h)', 'Pass Rate (%)'],
                        datasets: [{
                            data: [
                                data.metrics.totalRepos,
                                data.metrics.totalReleases,
                                data.metrics.totalCommits,
                                data.metrics.avgLeadTimeHours,
                                data.metrics.workflowPassRate,
                            ],
                            backgroundColor: ['#58a6ff', '#3fb950', '#d29922', '#f85149', '#3fb950'],
                        }],
                    },
                    options: {
                        responsive: true,
                        plugins: {
                            legend: { display: false },
                            title: { display: true, text: 'DORA Metrics Summary', color: '#f0f6fc' },
                        },
                        scales: {
                            y: { beginAtZero: true, grid: { color: '#21262d' }, ticks: { color: '#8b949e' } },
                            x: { grid: { display: false }, ticks: { color: '#8b949e' } },
                        },
                    },
                });
            }
        })
        .catch((e: Error) => console.error('Failed to load chart data:', e));
}


// E-ink mode: charts render as flat black/white without animation or tooltips.
export function initEinkCharts(): void {
    const html = document.documentElement;
    if (!html.classList.contains('eink-mode')) return;
    (window as any).Chart?.defaults?.set?.('animation', false);
    (window as any).Chart?.defaults?.set?.('responsive', true);
    (window as any).Chart?.defaults?.set?.('devicePixelRatio', 1);
    (window as any).Chart?.defaults?.set?.('interaction', { mode: 'index', intersect: false, enabled: false });
    (window as any).Chart?.defaults?.set?.('plugins', { tooltip: { enabled: false }, legend: { display: true } });
    if ((window as any).htmx) {
        (window as any).htmx.on('htmx:afterSwap', function () { (window as any).initEinkCharts && (window as any).initEinkCharts(); });
    }
}
if (typeof document !== 'undefined') {
    new MutationObserver(function () { (window as any).initEinkCharts && (window as any).initEinkCharts(); }).observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });
    (window as any).initEinkCharts = initEinkCharts;
    initEinkCharts();
}
