import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import {
  Search,
  ChevronUp,
  ChevronDown,
  X,
  Copy,
  Check,
  Maximize2,
  Minimize2,
  ArrowDown,
  Terminal,
  RotateCcw,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';

export interface LogViewerProps {
  logs: string[];
  isLoading?: boolean;
  isStreaming?: boolean;
  className?: string;
  height?: string | number;
  title?: string;
  emptyMessage?: string;
  onClear?: () => void;
  showLineNumbers?: boolean;
}

interface AnsiSegment {
  text: string;
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Strips ANSI color and control escape sequences.
 */
export function stripAnsi(str: string): string {
  // Matches standard ANSI CSI sequences, e.g. \u001b[31m, \u001b[0m, \u001b[2K, etc.
  return str.replace(/\r/g, '').replace(/(?:\u001b|\\033|\\e)\[[0-9;]*[a-zA-Z]/g, '');
}

/**
 * Parses a single line containing ANSI escape sequences into styled segments.
 */
function parseAnsiLine(rawLine: string): AnsiSegment[] {
  if (!rawLine) return [{ text: '' }];

  const line = rawLine.replace(/\r/g, '');
  const regex = /(?:\u001b|\\033|\\e)\[([0-9;]*)([a-zA-Z])/g;
  const segments: AnsiSegment[] = [];
  let lastIndex = 0;

  // Active styling state
  let bold = false;
  let dim = false;
  let italic = false;
  let underline = false;
  let color: string | undefined = undefined;
  let bgColor: string | undefined = undefined;

  const colorMap: Record<number, string> = {
    30: '#666666', // black / dark gray
    31: '#ff6369', // red
    32: '#46a758', // green
    33: '#ff9900', // yellow
    34: '#52a8ff', // blue
    35: '#d175ff', // magenta
    36: '#50e3c2', // cyan
    37: '#ededed', // white
    90: '#888888', // bright black
    91: '#ff858a', // bright red
    92: '#5ecc72', // bright green
    93: '#ffc53d', // bright yellow
    94: '#75baff', // bright blue
    95: '#e288ff', // bright magenta
    96: '#79ffe1', // bright cyan
    97: '#ffffff', // bright white
  };

  const bgMap: Record<number, string> = {
    40: '#111111',
    41: '#4d1b1e',
    42: '#13391e',
    43: '#452b0b',
    44: '#0d2847',
    45: '#3b1c47',
    46: '#0c3835',
    47: '#333333',
  };

  /**
   * Converts an ANSI 256-color index (0-255) to an RGB string.
   */
  const ansi256ToColor = (code: number): string => {
    if (code < 16) {
      return colorMap[code < 8 ? 30 + code : 90 + (code - 8)] || '#ededed';
    }
    if (code >= 16 && code <= 231) {
      const idx = code - 16;
      const r = Math.floor(idx / 36);
      const g = Math.floor((idx % 36) / 6);
      const b = idx % 6;
      return `rgb(${r ? r * 40 + 55 : 0}, ${g ? g * 40 + 55 : 0}, ${b ? b * 40 + 55 : 0})`;
    }
    if (code >= 232 && code <= 255) {
      const gray = (code - 232) * 10 + 8;
      return `rgb(${gray}, ${gray}, ${gray})`;
    }
    return '#ededed';
  };

  let match: RegExpExecArray | null;
  while ((match = regex.exec(line)) !== null) {
    // Push preceding text segment if any
    if (match.index > lastIndex) {
      const text = line.substring(lastIndex, match.index);
      if (text) {
        segments.push({
          text,
          style: {
            color,
            backgroundColor: bgColor,
            fontWeight: bold ? 600 : undefined,
            opacity: dim ? 0.65 : undefined,
            fontStyle: italic ? 'italic' : undefined,
            textDecoration: underline ? 'underline' : undefined,
          },
        });
      }
    }

    const command = match[2];
    if (command === 'm') {
      // Process color & text styling escape codes
      const codeStr = match[1];
      const codes = codeStr ? codeStr.split(';').map((c) => parseInt(c, 10)) : [0];

      for (let i = 0; i < codes.length; i++) {
        const code = isNaN(codes[i]) ? 0 : codes[i];
        if (code === 0) {
          // Reset
          bold = false;
          dim = false;
          italic = false;
          underline = false;
          color = undefined;
          bgColor = undefined;
        } else if (code === 1) {
          bold = true;
        } else if (code === 2) {
          dim = true;
        } else if (code === 3) {
          italic = true;
        } else if (code === 4) {
          underline = true;
        } else if (code === 22) {
          bold = false;
          dim = false;
        } else if (code === 23) {
          italic = false;
        } else if (code === 24) {
          underline = false;
        } else if (code === 38 && codes[i + 1] === 2 && i + 4 < codes.length) {
          // 24-bit Truecolor foreground
          color = `rgb(${codes[i + 2]}, ${codes[i + 3]}, ${codes[i + 4]})`;
          i += 4;
        } else if (code === 38 && codes[i + 1] === 5 && i + 2 < codes.length) {
          // 256-color foreground
          color = ansi256ToColor(codes[i + 2]);
          i += 2;
        } else if (code === 48 && codes[i + 1] === 2 && i + 4 < codes.length) {
          // 24-bit Truecolor background
          bgColor = `rgb(${codes[i + 2]}, ${codes[i + 3]}, ${codes[i + 4]})`;
          i += 4;
        } else if (code === 48 && codes[i + 1] === 5 && i + 2 < codes.length) {
          // 256-color background
          bgColor = ansi256ToColor(codes[i + 2]);
          i += 2;
        } else if (code >= 30 && code <= 37) {
          color = colorMap[code];
        } else if (code === 39) {
          color = undefined;
        } else if (code >= 40 && code <= 47) {
          bgColor = bgMap[code];
        } else if (code === 49) {
          bgColor = undefined;
        } else if (code >= 90 && code <= 97) {
          color = colorMap[code];
        }
      }
    }

    lastIndex = regex.lastIndex;
  }

  // Push remaining text
  if (lastIndex < line.length) {
    const text = line.substring(lastIndex);
    if (text) {
      segments.push({
        text,
        style: {
          color,
          backgroundColor: bgColor,
          fontWeight: bold ? 600 : undefined,
          opacity: dim ? 0.65 : undefined,
          fontStyle: italic ? 'italic' : undefined,
          textDecoration: underline ? 'underline' : undefined,
        },
      });
    }
  }

  return segments.length > 0 ? segments : [{ text: '' }];
}

const LINE_HEIGHT = 24;
const OVERSCAN = 15;

export function LogViewer({
  logs = [],
  isLoading = false,
  isStreaming = false,
  className,
  height = 480,
  title = 'Console Output',
  emptyMessage = 'No log output recorded.',
  onClear,
  showLineNumbers = true,
}: LogViewerProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);

  // States
  const [isStickyPinned, setIsStickyPinned] = useState(true);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [isSearchOpen, setIsSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [activeMatchIndex, setActiveMatchIndex] = useState(0);
  const [copied, setCopied] = useState(false);

  // Virtual windowing states
  const [scrollTop, setScrollTop] = useState(0);
  const [containerHeight, setContainerHeight] = useState(480);

  // Incremental ANSI parser cache: only parse newly appended lines
  const parsedCacheRef = useRef<AnsiSegment[][]>([]);
  const prevFirstLineRef = useRef<string | null>(null);
  const prevLastLineRef = useRef<string | null>(null);

  const parsedLines = useMemo(() => {
    const cached = parsedCacheRef.current;
    const isExtension =
      cached.length > 0 &&
      logs.length > cached.length &&
      logs[0] === prevFirstLineRef.current &&
      logs[cached.length - 1] === prevLastLineRef.current;

    if (isExtension) {
      const newSegments = logs.slice(cached.length).map((line) => parseAnsiLine(line));
      parsedCacheRef.current = [...cached, ...newSegments];
    } else {
      // Brand new logs array, step switched, reset, or non-extension
      parsedCacheRef.current = logs.map((line) => parseAnsiLine(line));
    }

    if (logs.length > 0) {
      prevFirstLineRef.current = logs[0];
      prevLastLineRef.current = logs[logs.length - 1];
    } else {
      prevFirstLineRef.current = null;
      prevLastLineRef.current = null;
    }

    return parsedCacheRef.current;
  }, [logs]);

  // Synchronize container height on mount/resize/fullscreen using ResizeObserver
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    setContainerHeight(el.clientHeight || 480);

    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        if (entry.contentRect.height > 0) {
          setContainerHeight(entry.contentRect.height);
        }
      }
    });
    ro.observe(el);

    return () => ro.disconnect();
  }, [isFullscreen, height]);

  // Search match positions: line indices matching the query
  const searchMatches = useMemo(() => {
    if (!searchQuery.trim()) return [];
    const query = searchQuery.toLowerCase();
    const matches: number[] = [];
    logs.forEach((rawLine, idx) => {
      const clean = stripAnsi(rawLine).toLowerCase();
      if (clean.includes(query)) {
        matches.push(idx);
      }
    });
    return matches;
  }, [logs, searchQuery]);

  // Reset active match index when query changes
  useEffect(() => {
    setActiveMatchIndex(0);
  }, [searchQuery]);

  // Jump to active match using virtual scroll offset calculation
  useEffect(() => {
    if (searchMatches.length > 0 && searchMatches[activeMatchIndex] !== undefined) {
      const lineIdx = searchMatches[activeMatchIndex];
      if (containerRef.current) {
        const targetScrollTop = Math.max(0, lineIdx * LINE_HEIGHT - containerHeight / 2);
        containerRef.current.scrollTo({ top: targetScrollTop, behavior: 'smooth' });
      }
    }
  }, [searchMatches, activeMatchIndex, containerHeight]);

  // Sticky autoscroll on new logs
  useEffect(() => {
    if (isStickyPinned && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [logs, isStickyPinned]);

  // Track scroll position to manage sticky pin and virtual window
  const handleScroll = useCallback(() => {
    if (!containerRef.current) return;
    const { scrollTop: st, scrollHeight, clientHeight } = containerRef.current;
    setScrollTop(st);
    setContainerHeight(clientHeight);
    const atBottom = scrollHeight - st - clientHeight < 40;
    setIsStickyPinned(atBottom);
  }, []);

  const scrollToBottom = () => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
      setIsStickyPinned(true);
    }
  };

  // Virtual windowing calculations
  const totalLines = logs.length;
  const totalHeight = totalLines * LINE_HEIGHT;
  const startIndex = Math.max(0, Math.floor(scrollTop / LINE_HEIGHT) - OVERSCAN);
  const endIndex = Math.min(
    totalLines,
    Math.ceil((scrollTop + containerHeight) / LINE_HEIGHT) + OVERSCAN
  );

  const visibleLines = useMemo(() => {
    const result: Array<{ index: number; segments: AnsiSegment[] }> = [];
    for (let i = startIndex; i < endIndex; i++) {
      result.push({
        index: i,
        segments: parsedLines[i] || [{ text: '' }],
      });
    }
    return result;
  }, [startIndex, endIndex, parsedLines]);

  const offsetY = startIndex * LINE_HEIGHT;

  // Keyboard shortcuts (Cmd+F / Ctrl+F, 'z' for zoom/fullscreen, Esc)
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Check if user is typing in another input/textarea
      const targetTag = (e.target as HTMLElement)?.tagName?.toLowerCase();
      const isInput = targetTag === 'input' || targetTag === 'textarea';

      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'f') {
        e.preventDefault();
        setIsSearchOpen(true);
        setTimeout(() => searchInputRef.current?.focus(), 50);
        return;
      }

      if (e.key === 'Escape') {
        if (isSearchOpen) {
          setIsSearchOpen(false);
          setSearchQuery('');
        } else if (isFullscreen) {
          setIsFullscreen(false);
        }
        return;
      }

      if (e.key === 'z' && !isInput && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        setIsFullscreen((prev) => !prev);
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isSearchOpen, isFullscreen]);

  // Search navigation handlers
  const handleNextMatch = () => {
    if (searchMatches.length === 0) return;
    setActiveMatchIndex((prev) => (prev + 1) % searchMatches.length);
  };

  const handlePrevMatch = () => {
    if (searchMatches.length === 0) return;
    setActiveMatchIndex((prev) => (prev - 1 + searchMatches.length) % searchMatches.length);
  };

  // Copy logs to clipboard
  const handleCopy = async () => {
    try {
      const cleanText = logs.map(stripAnsi).join('\n');
      await navigator.clipboard.writeText(cleanText);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.warn('Failed to copy logs to clipboard:', err);
    }
  };

  // Highlight search term inside text
  const renderHighlightedText = (text: string, query: string, style?: React.CSSProperties) => {
    if (!query.trim()) {
      return <span style={style}>{text}</span>;
    }

    const lowerText = text.toLowerCase();
    const lowerQuery = query.toLowerCase();
    const parts: React.ReactNode[] = [];
    let start = 0;
    let idx = lowerText.indexOf(lowerQuery, start);

    while (idx !== -1) {
      if (idx > start) {
        parts.push(
          <span key={`txt-${start}`} style={style}>
            {text.substring(start, idx)}
          </span>
        );
      }
      parts.push(
        <mark
          key={`mark-${idx}`}
          className="bg-[#f5a623]/35 text-white font-semibold rounded px-0.5 border border-[#f5a623]/60"
        >
          {text.substring(idx, idx + query.length)}
        </mark>
      );
      start = idx + query.length;
      idx = lowerText.indexOf(lowerQuery, start);
    }

    if (start < text.length) {
      parts.push(
        <span key={`txt-${start}`} style={style}>
          {text.substring(start)}
        </span>
      );
    }

    return parts;
  };

  return (
    <div
      className={cn(
        'rounded-geist bg-geist-surface border border-geist-hairline flex flex-col overflow-hidden font-mono select-text transition-all duration-200',
        isFullscreen
          ? 'fixed inset-0 z-50 rounded-none border-none bg-geist-canvas h-screen w-screen p-4'
          : 'h-[520px]',
        className
      )}
    >
      {/* Top Console Toolbar */}
      <div className="h-10 px-3 bg-geist-elevated border-b border-geist-hairline flex items-center justify-between flex-shrink-0 select-none">
        <div className="flex items-center gap-2">
          <Terminal className="w-3.5 h-3.5 text-geist-blue" />
          <span className="text-xs font-semibold text-white tracking-tight">{title}</span>
          {isStreaming && (
            <span className="flex items-center gap-1.5 ml-2 text-[10px] text-status-success-text">
              <span className="w-1.5 h-1.5 rounded-full bg-status-success-solid animate-pulse" />
              Live
            </span>
          )}
          <span className="text-[11px] text-geist-faint ml-1">
            ({logs.length} line{logs.length === 1 ? '' : 's'})
          </span>
        </div>

        {/* Action Controls */}
        <div className="flex items-center gap-1">
          {/* Search Trigger */}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setIsSearchOpen((prev) => !prev);
              if (!isSearchOpen) {
                setTimeout(() => searchInputRef.current?.focus(), 50);
              }
            }}
            className={cn(
              'h-8 px-2.5 text-xs text-geist-secondary hover:text-white',
              isSearchOpen && 'bg-geist-hover text-white'
            )}
            title="Search logs (Cmd+F)"
            aria-label="Search logs"
          >
            <Search className="w-3.5 h-3.5" />
          </Button>

          {/* Copy Button */}
          <Button
            variant="ghost"
            size="sm"
            onClick={handleCopy}
            disabled={logs.length === 0}
            className="h-8 px-2.5 text-xs text-geist-secondary hover:text-white gap-1"
            title="Copy all logs"
            aria-label="Copy logs to clipboard"
          >
            {copied ? (
              <>
                <Check className="w-3.5 h-3.5 text-status-success-text" />
                <span className="text-[10px] text-status-success-text">Copied</span>
              </>
            ) : (
              <>
                <Copy className="w-3.5 h-3.5" />
                <span className="text-[10px] hidden sm:inline">Copy</span>
              </>
            )}
          </Button>

          {/* Clear Logs if callback provided */}
          {onClear && (
            <Button
              variant="ghost"
              size="sm"
              onClick={onClear}
              disabled={logs.length === 0}
              className="h-8 px-2.5 text-xs text-geist-secondary hover:text-white"
              title="Clear output"
              aria-label="Clear log output"
            >
              <RotateCcw className="w-3.5 h-3.5" />
            </Button>
          )}

          {/* Fullscreen Zoom Toggle */}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setIsFullscreen((prev) => !prev)}
            className="h-8 px-2.5 text-xs text-geist-secondary hover:text-white"
            title={isFullscreen ? 'Exit full screen (z / Esc)' : 'Full screen (z)'}
            aria-label={isFullscreen ? 'Exit full screen' : 'Full screen'}
          >
            {isFullscreen ? <Minimize2 className="w-3.5 h-3.5" /> : <Maximize2 className="w-3.5 h-3.5" />}
          </Button>
        </div>
      </div>

      {/* Embedded Search Bar */}
      {isSearchOpen && (
        <div className="h-9 px-3 bg-geist-surface border-b border-geist-hairline flex items-center justify-between gap-2 flex-shrink-0 animate-in fade-in slide-in-from-top-1 duration-150">
          <div className="flex items-center gap-2 flex-1">
            <Search className="w-3.5 h-3.5 text-geist-faint flex-shrink-0" />
            <input
              ref={searchInputRef}
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  if (e.shiftKey) {
                    handlePrevMatch();
                  } else {
                    handleNextMatch();
                  }
                }
              }}
              placeholder="Filter or search in console output..."
              className="bg-transparent text-xs text-white placeholder-geist-faint focus:outline-none w-full"
            />
          </div>

          <div className="flex items-center gap-2 flex-shrink-0">
            <span className="text-[11px] text-geist-faint font-mono">
              {searchQuery.trim()
                ? searchMatches.length > 0
                  ? `${activeMatchIndex + 1} of ${searchMatches.length} matches`
                  : 'No matches'
                : ''}
            </span>

            <div className="flex items-center gap-0.5">
              <Button
                variant="ghost"
                size="sm"
                onClick={handlePrevMatch}
                disabled={searchMatches.length === 0}
                className="h-6 w-6 p-0 text-geist-secondary hover:text-white"
                title="Previous match (Shift+Enter)"
              >
                <ChevronUp className="w-3.5 h-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={handleNextMatch}
                disabled={searchMatches.length === 0}
                className="h-6 w-6 p-0 text-geist-secondary hover:text-white"
                title="Next match (Enter)"
              >
                <ChevronDown className="w-3.5 h-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setIsSearchOpen(false);
                  setSearchQuery('');
                }}
                className="h-6 w-6 p-0 text-geist-secondary hover:text-white ml-1"
                title="Close search (Esc)"
              >
                <X className="w-3.5 h-3.5" />
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* Main Console Lines Body */}
      <div
        ref={containerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto overflow-x-auto p-3 text-xs font-mono bg-[#0a0a0a] relative select-text"
      >
        {logs.length === 0 ? (
          <div className="h-full min-h-[160px] flex flex-col items-center justify-center text-center text-geist-faint space-y-2 select-none">
            {isLoading ? (
              <>
                <div className="w-5 h-5 border-2 border-geist-hairline border-t-geist-blue rounded-full animate-spin" />
                <p className="text-xs text-geist-secondary font-medium">Loading console stream...</p>
              </>
            ) : isStreaming ? (
              <>
                <div className="w-2 h-2 rounded-full bg-status-success-solid animate-ping" />
                <p className="text-xs text-geist-secondary font-medium">
                  Waiting for runner process output...
                </p>
                <p className="text-[11px] text-geist-faint">
                  Logs will stream into this console automatically.
                </p>
              </>
            ) : (
              <>
                <Terminal className="w-8 h-8 text-geist-hairline" />
                <p className="text-xs text-geist-secondary">{emptyMessage}</p>
              </>
            )}
          </div>
        ) : (
          <div
            style={{ height: totalHeight }}
            className="font-mono text-xs relative w-full min-w-max"
          >
            <div
              style={{ transform: `translateY(${offsetY}px)` }}
              className="absolute inset-x-0 top-0"
            >
              {visibleLines.map(({ index: lineIdx, segments }) => {
                const isMatch = searchMatches.includes(lineIdx);
                const isCurrentMatch =
                  searchMatches.length > 0 && searchMatches[activeMatchIndex] === lineIdx;

                return (
                  <div
                    key={lineIdx}
                    id={`log-line-${lineIdx}`}
                    className={cn(
                      'h-6 leading-6 flex items-center hover:bg-geist-hover/60 transition-colors duration-75 text-[12px]',
                      isCurrentMatch && 'bg-status-warning-bg/40',
                      isMatch && !isCurrentMatch && 'bg-status-warning-bg/15'
                    )}
                  >
                    {/* Line Number Gutter */}
                    {showLineNumbers && (
                      <div className="select-none text-right pr-3 pl-1 text-[11px] text-geist-faint/70 w-12 flex-shrink-0 border-r border-geist-hairline/40 h-full flex items-center justify-end">
                        {lineIdx + 1}
                      </div>
                    )}

                    {/* Line Text Content */}
                    <div className="pl-3 pr-2 whitespace-pre flex-1 font-mono text-geist-ink flex items-center">
                      {segments.map((seg, segIdx) => (
                        <React.Fragment key={segIdx}>
                          {renderHighlightedText(seg.text, searchQuery, seg.style)}
                        </React.Fragment>
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* Floating Autoscroll Release / Resume Button */}
        {!isStickyPinned && logs.length > 10 && (
          <button
            onClick={scrollToBottom}
            className="sticky bottom-2 ml-auto mr-2 flex items-center gap-1.5 px-3 py-1.5 rounded-full bg-geist-elevated/90 hover:bg-geist-hover border border-geist-hairline text-white text-[11px] font-sans font-medium shadow-xl backdrop-blur transition-all duration-150 cursor-pointer group"
            aria-label="Resume autoscroll to bottom"
          >
            <ArrowDown className="w-3.5 h-3.5 text-geist-blue transition-transform duration-200 group-hover:translate-y-0.5" />
            <span>Resume autoscroll</span>
          </button>
        )}
      </div>
    </div>
  );
}
