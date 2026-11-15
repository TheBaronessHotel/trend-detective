import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

// AI models answer in Markdown. react-markdown builds React elements, never
// raw HTML: HTML in an answer shows as text, and links to javascript: and
// other unsafe schemes are dropped.
const components: Components = {
  // The panel's own title is an h3, so the answer's headings sit under it.
  h1: ({ node: _node, ...props }) => <h4 className="md-h" {...props} />,
  h2: ({ node: _node, ...props }) => <h4 className="md-h" {...props} />,
  h3: ({ node: _node, ...props }) => <h5 className="md-h" {...props} />,
  h4: ({ node: _node, ...props }) => <h5 className="md-h" {...props} />,
  h5: ({ node: _node, ...props }) => <h6 className="md-h" {...props} />,
  h6: ({ node: _node, ...props }) => <h6 className="md-h" {...props} />,
  a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noreferrer noopener" />,
  // Wide tables scroll inside the panel instead of stretching it.
  table: ({ node: _node, ...props }) => (
    <div className="md-table">
      <table {...props} />
    </div>
  ),
};

/** An assistant's answer, rendered from Markdown, with tables (GitHub style). */
export default function Markdown({ text }: { text: string }) {
  return (
    <div className="md">
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {text}
      </ReactMarkdown>
    </div>
  );
}
