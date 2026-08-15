interface SnapshotData {
    id: number;
    repo_id: number;
    repo_name: string;
    timestamp: string;
    feat_count: number;
    fix_count: number;
    docs_count: number;
    chore_count: number;
    other_commit_count: number;
    total_commits_fetched: number;
    release_count: number;
    avg_lead_time_hours: number;
    workflow_success_count: number;
    workflow_failure_count: number;
    workflow_status: string;
}

interface SnapshotsResponse {
    snapshots: SnapshotData[];
}

export interface TrendBucket {
    timestamp: Date;
    feat_count: number;
    fix_count: number;
    docs_count: number;
    chore_count: number;
    other_commit_count: number;
    release_count: number;
    workflow_success_count: number;
    workflow_failure_count: number;
    avg_lead_time_hours: number | null;
}

interface ChartInstances {
    [key: string]: any;
}

const trendChartInstances: ChartInstances = {};

function destroyTrendCharts(): void {
    Object.values(trendChartInstances).forEach(c => {
        try { c.destroy(); } catch (_e) { /* ignore */ }
    });
    for (const key in trendChartInstances) {
        delete trendChartInstances[key];
    }
}

function getSinceDate(range: string): Date | null {
    const now = new Date();
    switch (range) {
        case '24h': return new Date(now.getTime() - 24 * 60 * 60 * 1000);
        case '7d': return new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000);
        case '30d': return new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000);
        case '90d': return new Date(now.getTime() - 90 * 24 * 60 * 60 * 1000);
        default: return null;
    }
}

function buildTrendsUrl(): string {
    const rangeSelect = document.getElementById('trends-range') as HTMLSelectElement | null;
    const repoSelect = document.getElementById('trends-repo') as HTMLSelectElement | null;
    const range = rangeSelect?.value || '7d';
    const repoId = repoSelect?.value || '';

    const params = new URLSearchParams();
    const since = getSinceDate(range);
    if (since) params.set('since', since.toISOString());
    if (repoId) params.set('repo_id', repoId);
    return '/metrics/history?' + params.toString();
}

function selectedRange(): string {
    return (document.getElementById('trends-range') as HTMLSelectElement | null)?.value || '7d';
}

function bucketStart(date: Date, range: string): Date {
    const result = new Date(date);
    if (range === '24h') {
        result.setMinutes(0, 0, 0);
    } else if (range === '90d' || range === 'all') {
        result.setHours(0, 0, 0, 0);
        const day = result.getDay();
        result.setDate(result.getDate() - (day === 0 ? 6 : day - 1));
    } else {
        result.setHours(0, 0, 0, 0);
    }
    return result;
}

function numeric(value: unknown): number {
    return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

/** Reduce repository snapshots into display buckets without unweighted rate averages. */
export function aggregateSnapshots(snapshots: SnapshotData[], range: string): TrendBucket[] {
    const buckets = new Map<number, TrendBucket & { leadTimeTotal: number; leadTimeCount: number }>();
    for (const snapshot of snapshots) {
        const date = new Date(snapshot.timestamp);
        if (Number.isNaN(date.getTime())) continue;
        const timestamp = bucketStart(date, range);
        const key = timestamp.getTime();
        let bucket = buckets.get(key);
        if (!bucket) {
            bucket = {
                timestamp,
                feat_count: 0,
                fix_count: 0,
                docs_count: 0,
                chore_count: 0,
                other_commit_count: 0,
                release_count: 0,
                workflow_success_count: 0,
                workflow_failure_count: 0,
                avg_lead_time_hours: null,
                leadTimeTotal: 0,
                leadTimeCount: 0,
            };
            buckets.set(key, bucket);
        }
        bucket.feat_count += numeric(snapshot.feat_count);
        bucket.fix_count += numeric(snapshot.fix_count);
        bucket.docs_count += numeric(snapshot.docs_count);
        bucket.chore_count += numeric(snapshot.chore_count);
        bucket.other_commit_count += numeric(snapshot.other_commit_count);
        bucket.release_count += numeric(snapshot.release_count);
        bucket.workflow_success_count += numeric(snapshot.workflow_success_count);
        bucket.workflow_failure_count += numeric(snapshot.workflow_failure_count);
        const leadTime = numeric(snapshot.avg_lead_time_hours);
        if (leadTime > 0) {
            bucket.leadTimeTotal += leadTime;
            bucket.leadTimeCount++;
        }
    }

    return [...buckets.values()]
        .sort((a, b) => a.timestamp.getTime() - b.timestamp.getTime())
        .map(({ leadTimeTotal, leadTimeCount, ...bucket }) => ({
            ...bucket,
            avg_lead_time_hours: leadTimeCount > 0 ? leadTimeTotal / leadTimeCount : null,
        }));
}

function setTrendEmptyState(empty: boolean): void {
    document.querySelectorAll<HTMLElement>('[data-trend-chart-card]').forEach(card => {
        const canvas = card.querySelector('canvas') as HTMLCanvasElement | null;
        const message = card.querySelector<HTMLElement>('[data-trend-empty]');
        if (canvas) canvas.classList.toggle('d-none', empty);
        if (message) message.classList.toggle('d-none', !empty);
    });
}

export function loadTrendsData(): void {
    const url = buildTrendsUrl();
    destroyTrendCharts();
    setTrendEmptyState(false);
    fetch(url)
        .then(response => {
            if (!response.ok) throw new Error(`HTTP ${response.status}`);
            return response.json();
        })
        .then((data: SnapshotsResponse) => {
            if (!data || !Array.isArray(data.snapshots)) throw new Error('Invalid trends response');
            const buckets = aggregateSnapshots(data.snapshots, selectedRange());
            if (buckets.length === 0) {
                setTrendEmptyState(true);
                return;
            }
            setTrendEmptyState(false);
            buildCommitTrendChart(buckets);
            buildWorkflowTrendChart(buckets);
            buildLeadTimeTrendChart(buckets);
            buildReleaseTrendChart(buckets);
        })
        .catch((e: Error) => {
            destroyTrendCharts();
            setTrendEmptyState(true);
            console.error('Failed to load trends data:', e);
        });
}

function timeScale(range: string, maxTicksLimit: number): any {
    const unit = range === '24h' ? 'hour' : (range === '90d' || range === 'all' ? 'week' : 'day');
    return {
        type: 'time',
        time: { unit, tooltipFormat: unit === 'hour' ? 'MMM d, yyyy HH:mm' : 'MMM d, yyyy' },
        grid: { color: '#21262d' },
        ticks: { color: '#8b949e', maxTicksLimit },
    };
}

function baseLineOptions(range: string, maxTicksLimit: number): any {
    return {
        responsive: true,
        maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        plugins: { legend: { labels: { color: '#8b949e', boxWidth: 12 } } },
        scales: { x: timeScale(range, maxTicksLimit) },
    };
}

function buildCommitTrendChart(buckets: TrendBucket[]): void {
    const ctx = document.getElementById('commitTrendChart') as HTMLCanvasElement | null;
    if (!ctx) return;
    const range = selectedRange();
    const colors = ['#3fb950', '#f85149', '#d29922', '#8b949e', '#6e7681'];
    const names: Array<[string, keyof TrendBucket]> = [
        ['Features', 'feat_count'], ['Fixes', 'fix_count'], ['Docs', 'docs_count'],
        ['Chore', 'chore_count'], ['Other', 'other_commit_count'],
    ];
    trendChartInstances.commit = new (window as any).Chart(ctx, {
        type: 'line',
        data: {
            labels: buckets.map(b => b.timestamp),
            datasets: names.map(([label, key], index) => ({
                label, data: buckets.map(b => b[key] as number), backgroundColor: colors[index],
                borderColor: colors[index], fill: false, tension: 0.2, pointRadius: 0, pointHitRadius: 8,
            })),
        },
        options: {
            ...baseLineOptions(range, 10),
            plugins: { legend: { labels: { color: '#8b949e', boxWidth: 12 } } },
            scales: {
                x: timeScale(range, 10),
                y: { beginAtZero: true, grid: { color: '#21262d' }, ticks: { color: '#8b949e', precision: 0 } },
            },
        },
    });
}

function buildWorkflowTrendChart(buckets: TrendBucket[]): void {
    const ctx = document.getElementById('workflowTrendChart') as HTMLCanvasElement | null;
    if (!ctx) return;
    const passRate = buckets.map(b => {
        const total = b.workflow_success_count + b.workflow_failure_count;
        return total > 0 ? (b.workflow_success_count / total) * 100 : null;
    });
    const options = baseLineOptions(selectedRange(), 8);
    options.scales = {
        x: timeScale(selectedRange(), 8), y: { min: 0, max: 100, grid: { color: '#21262d' },
            ticks: { color: '#8b949e', callback: (v: any) => v + '%' } },
    };
    trendChartInstances.workflow = new (window as any).Chart(ctx, {
        type: 'line', data: { labels: buckets.map(b => b.timestamp), datasets: [{
            label: 'Pass Rate (%)', data: passRate, backgroundColor: '#3fb950', borderColor: '#3fb950',
            fill: false, tension: 0.2, pointRadius: 1, pointHitRadius: 8, spanGaps: true,
        }] }, options,
    });
}

function buildLeadTimeTrendChart(buckets: TrendBucket[]): void {
    const ctx = document.getElementById('leadTimeTrendChart') as HTMLCanvasElement | null;
    if (!ctx) return;
    const values = buckets.map(b => b.avg_lead_time_hours);
    const valid = values.filter((value): value is number => value !== null);
    const max = Math.max(...valid, 0);
    const options = baseLineOptions(selectedRange(), 8);
    options.scales = {
        x: timeScale(selectedRange(), 8),
        y: { beginAtZero: true, suggestedMax: max > 0 ? max * 1.15 : 1, grid: { color: '#21262d' },
            ticks: { color: '#8b949e', callback: (v: any) => `${Number(v).toFixed(2)}h` } },
    };
    trendChartInstances.leadTime = new (window as any).Chart(ctx, {
        type: 'line', data: { labels: buckets.map(b => b.timestamp), datasets: [{
            label: 'Avg Lead Time (h)', data: values, backgroundColor: '#58a6ff', borderColor: '#58a6ff',
            fill: false, tension: 0.2, pointRadius: 1, pointHitRadius: 8, spanGaps: true,
        }] }, options,
    });
}

function buildReleaseTrendChart(buckets: TrendBucket[]): void {
    const ctx = document.getElementById('releaseTrendChart') as HTMLCanvasElement | null;
    if (!ctx) return;
    trendChartInstances.release = new (window as any).Chart(ctx, {
        type: 'bar',
        data: { labels: buckets.map(b => b.timestamp), datasets: [{
            label: 'Releases', data: buckets.map(b => b.release_count), backgroundColor: '#58a6ff', borderColor: '#58a6ff', borderWidth: 1,
        }] },
        options: {
            responsive: true, maintainAspectRatio: false,
            plugins: { legend: { labels: { color: '#8b949e' } } },
            scales: {
                x: timeScale(selectedRange(), 12),
                y: { beginAtZero: true, grid: { color: '#21262d' }, ticks: { stepSize: 1, precision: 0, color: '#8b949e' } },
            },
        },
    });
}
