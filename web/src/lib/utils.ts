import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatBytes(bytes: number, decimals = 1): string {
  if (!bytes || bytes <= 0) return '0 B';
  const k = 1024;
  const dm = decimals < 0 ? 0 : decimals;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  const safeI = Math.max(0, Math.min(i, sizes.length - 1));
  return `${parseFloat((bytes / Math.pow(k, safeI)).toFixed(dm))} ${sizes[safeI]}`;
}

export function formatRate(bytesPerSec?: number, decimals = 1): string {
  if (!bytesPerSec || bytesPerSec <= 0) return '0 B/s';
  const k = 1024;
  const dm = decimals < 0 ? 0 : decimals;
  const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'];
  const i = Math.floor(Math.log(bytesPerSec) / Math.log(k));
  const safeI = Math.max(0, Math.min(i, sizes.length - 1));
  return `${parseFloat((bytesPerSec / Math.pow(k, safeI)).toFixed(dm))} ${sizes[safeI]}`;
}

export function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return '0s';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

export function formatRelativeTime(dateStr: string): string {
  if (!dateStr) return '-';
  const date = new Date(dateStr);
  const now = new Date();
  const diffSec = Math.floor((now.getTime() - date.getTime()) / 1000);
  if (diffSec < 60) return `${diffSec}s ago`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHour = Math.floor(diffMin / 60);
  if (diffHour < 24) return `${diffHour}h ago`;
  const diffDays = Math.floor(diffHour / 24);
  return `${diffDays}d ago`;
}

export function formatAppVersion(raw?: string): { display: string; full: string } {
  if (!raw || raw === 'dev') return { display: 'v0.2.0-dev', full: raw || 'dev' };
  let clean = raw;
  while (clean.startsWith('vv')) {
    clean = clean.slice(1);
  }
  const match = clean.match(/^(v\d+\.\d+\.\d+)/);
  if (match) {
    const isDirty = clean.includes('dirty');
    const isAhead = clean.includes('-g') || clean.split('-').length > 2;
    const display = (isDirty || isAhead) ? `${match[1]}-dev` : match[1];
    return {
      display,
      full: clean,
    };
  }
  return { display: clean.length > 10 ? `${clean.slice(0, 8)}…` : clean, full: clean };
}

export function getProgressWidthClass(percent: number): string {
  const clamped = Math.max(0, Math.min(100, Math.round(percent)));
  if (clamped <= 0) return 'w-0';
  if (clamped <= 5) return 'w-[5%]';
  if (clamped <= 10) return 'w-[10%]';
  if (clamped <= 15) return 'w-[15%]';
  if (clamped <= 20) return 'w-[20%]';
  if (clamped <= 25) return 'w-[25%]';
  if (clamped <= 30) return 'w-[30%]';
  if (clamped <= 35) return 'w-[35%]';
  if (clamped <= 40) return 'w-[40%]';
  if (clamped <= 45) return 'w-[45%]';
  if (clamped <= 50) return 'w-[50%]';
  if (clamped <= 55) return 'w-[55%]';
  if (clamped <= 60) return 'w-[60%]';
  if (clamped <= 65) return 'w-[65%]';
  if (clamped <= 70) return 'w-[70%]';
  if (clamped <= 75) return 'w-[75%]';
  if (clamped <= 80) return 'w-[80%]';
  if (clamped <= 85) return 'w-[85%]';
  if (clamped <= 90) return 'w-[90%]';
  if (clamped <= 95) return 'w-[95%]';
  return 'w-full';
}
