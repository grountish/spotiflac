 
import { HomeIcon } from 'lucide-react';
interface HeaderProps {
  onNavigateHome?: () => void;
}
export function Header({ onNavigateHome }: HeaderProps) {
  return (
    <div className="relative">
      <div className="text-center space-y-2">
        <div className="flex items-center justify-center gap-3">
          <button
            type="button"
            className="cursor-pointer transition-opacity hover:opacity-80"
            onClick={onNavigateHome}
            aria-label="Go to home">
            <HomeIcon size={28} />
          </button>
        </div>
      </div>
    </div>
  );
}
