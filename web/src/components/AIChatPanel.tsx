import { useState, useRef, useEffect } from 'react'
import { Send, X, Sparkles, Loader2, Check, Maximize2, Minimize2 } from 'lucide-react'
import { sendChatMessage, sendChatMessageStream, searchToolsInStore, searchToolsOnInternet, classifyIntent, extractMCPServerFromURL, installToolFromStore, installInternetMCP } from '../api/aiChat'
import { detectToolOfferFromAI, isUserConfirmingToolSearch, isUserRequestingInternetSearch, detectSearchRefinementFeedback } from '../utils/intentDetection'
import { StoreResultsPanel, InternetResultsPanel } from './AIChatToolCards'
import type { ToolInfo, InternetResult } from './AIChatToolCards'

// --- Type definitions ---

interface ChatMessage {
  role: string
  content: string
  isError?: boolean
  isStreaming?: boolean
  isSearching?: boolean
  proposedYaml?: string
  action?: string
  searchInfo?: { tool: string; query: string }
  data?: any
}

interface FocusedNode {
  name: string
  type: string
  [key: string]: any
}

interface AIChatPanelProps {
  isOpen: boolean
  onClose: () => void
  context?: string
  currentYaml?: string
  selectedNodes?: any[]
  focusedNode?: FocusedNode | null
  agentId?: string | null
  onApplyYaml?: (yaml: string) => void
  tools?: ToolInfo[]
  onToolsRefresh?: () => Promise<void>
}



export default function AIChatPanel({ 
  isOpen, 
  onClose, 
  context = 'create_flow',
  currentYaml = '',
  selectedNodes = [],
  focusedNode = null,
  agentId = null,
  onApplyYaml,
  tools = [],
  onToolsRefresh,
}: AIChatPanelProps) {
  // Separate message histories: flow chat preserves, node refiner resets
  const [flowMessages, setFlowMessages] = useState<ChatMessage[]>([])
  const [nodeMessages, setNodeMessages] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [isLoading, setIsLoading] = useState(false)
  const [_pendingYaml, setPendingYaml] = useState<string | null>(null)
  const [isExpanded, setIsExpanded] = useState(false)
  const [storeResults, setStoreResults] = useState<ToolInfo[] | null>(null) // Store search results
  const [internetResults, setInternetResults] = useState<InternetResult[] | null>(null) // Internet search results
  const [installingTool, setInstallingTool] = useState<string | null>(null) // Tool being installed
  const [pendingToolRequirement, setPendingToolRequirement] = useState<string | null>(null) // Stores requirement when AI asks for confirmation
  const [originalRequest, setOriginalRequest] = useState<string | null>(null) // Stores original user request for auto-continue after install
  const [currentRequirement, setCurrentRequirement] = useState<string | null>(null) // Current tool requirement being searched
  const messagesEndRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLTextAreaElement | null>(null)
  
  // Use the right message state based on context
  const isNodeContext = context === 'node_config' && focusedNode
  const messages = isNodeContext ? nodeMessages : flowMessages
  const setMessages = isNodeContext ? setNodeMessages : setFlowMessages

  // Scroll to bottom when new messages arrive
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, storeResults])

  // Focus input when panel opens
  useEffect(() => {
    if (isOpen) {
      inputRef.current?.focus()
    }
  }, [isOpen])

  // Reset flow chat when agent changes (context is different per agent)
  useEffect(() => {
    setFlowMessages([])
    setPendingYaml(null)
    setStoreResults(null)
  }, [agentId])

  // Reset node refiner when focused node changes (fresh start per node)
  useEffect(() => {
    if (focusedNode) {
      setNodeMessages([])
      setPendingYaml(null)
      setStoreResults(null)
    }
  }, [focusedNode])

  // Handle tool installation
  const handleInstallTool = async (tool: ToolInfo) => {
    setInstallingTool(tool.id || null)
    try {
      // Pass collected env vars if any
      const envVars = tool.collectedEnv || {}
      const result = await installToolFromStore(tool.id || '', tool.name.toLowerCase().replace(/\s+/g, '-'), envVars)
      if (result.status === 'ok') {
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `✓ Installed ${tool.name}! ${result.toolsLoaded} tools loaded.`
        }])
        setStoreResults(null) // Clear results after successful install
        
        // Auto-continue: retry the original request now that tool is installed
        if (originalRequest) {
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: '↻ Retrying your original request with the new tool...'
          }])
          
          // Small delay to let the message appear, then retry
          setTimeout(async () => {
            if (onToolsRefresh) await onToolsRefresh()
            setIsLoading(true)
            try {
              const history = messages.map(m => ({ role: m.role, content: m.content }))
              const response = await sendChatMessage(
                originalRequest!,
                context,
                currentYaml,
                selectedNodes,
                history
              )
              
              if (!response.error && response.proposedYaml && onApplyYaml) {
                setMessages(prev => [...prev, { 
                  role: 'assistant', 
                  content: response.message,
                  proposedYaml: response.proposedYaml,
                }])
                onApplyYaml(response.proposedYaml)
                setMessages(prev => [...prev, { 
                  role: 'system', 
                  content: '✓ Flow created! Use Undo (⌘Z) to revert if needed.' 
                }])
              } else if (response.message) {
                setMessages(prev => [...prev, { 
                  role: 'assistant', 
                  content: response.message,
                }])
              }
            } catch (err: any) {
              console.error('Auto-continue failed:', err)
            } finally {
              setIsLoading(false)
              setOriginalRequest(null)
            }
          }, 500)
        }
      } else {
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `⚠️ Installation issue: ${result.toolError || 'Unknown error'}`,
          isError: true
        }])
      }
    } catch (err: any) {
      setMessages(prev => [...prev, { 
        role: 'system', 
        content: `Error installing tool: ${err.message}`,
        isError: true
      }])
    } finally {
      setInstallingTool(null)
    }
  }
  
  // Handler for installing MCP server from internet search result
  const handleInstallInternetMCP = async (result: InternetResult, envValues: Record<string, string>) => {
    setInstallingTool(result.name)
    try {
      const response = await installInternetMCP(result, envValues)
      if (response.status === 'ok') {
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `✓ Installed ${result.name}! ${response.toolsLoaded || 0} tools loaded.`
        }])
        setInternetResults(null) // Clear results after successful install
        
        // Auto-continue: retry the original request now that tool is installed
        if (originalRequest) {
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: '↻ Retrying your original request with the new tool...'
          }])
          
          // Small delay to let the message appear, then retry
          setTimeout(async () => {
            if (onToolsRefresh) await onToolsRefresh()
            setIsLoading(true)
            try {
              const history = messages.map(m => ({ role: m.role, content: m.content }))
              const chatResponse = await sendChatMessage(
                originalRequest!,
                context,
                currentYaml,
                selectedNodes,
                history
              )
              
              if (!chatResponse.error && chatResponse.proposedYaml && onApplyYaml) {
                setMessages(prev => [...prev, { 
                  role: 'assistant', 
                  content: chatResponse.message,
                  proposedYaml: chatResponse.proposedYaml,
                }])
                onApplyYaml(chatResponse.proposedYaml)
                setMessages(prev => [...prev, { 
                  role: 'system', 
                  content: '✓ Flow created! Use Undo (⌘Z) to revert if needed.' 
                }])
              } else if (chatResponse.message) {
                setMessages(prev => [...prev, { 
                  role: 'assistant', 
                  content: chatResponse.message,
                }])
              }
            } catch (err: any) {
              console.error('Auto-continue failed:', err)
            } finally {
              setIsLoading(false)
              setOriginalRequest(null)
            }
          }, 500)
        }
      } else {
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `⚠️ Installation issue: ${response.error || 'Unknown error'}`,
          isError: true
        }])
      }
    } catch (err: any) {
      setMessages(prev => [...prev, { 
        role: 'system', 
        content: `Error installing ${result.name}: ${err.message}`,
        isError: true
      }])
    } finally {
      setInstallingTool(null)
    }
  }
  
  // Handler for triggering internet search from store results panel
  const handleSearchOnline = async () => {
    if (!currentRequirement) return
    
    setStoreResults(null) // Clear store results
    setIsLoading(true)
    
    // Show searching message immediately
    setMessages(prev => [...prev, { 
      role: 'system', 
      content: `🔍 Searching for MCP servers...`,
      isSearching: true
    }])
    
    try {
      const internetResult = await searchToolsOnInternet(currentRequirement)
      const toolName = internetResult.toolUsed || 'websearch'
      const actualQuery = internetResult.searchQuery || currentRequirement
      
      if (internetResult.results && internetResult.results.length > 0) {
        setInternetResults(internetResult.results)
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `✅ Found ${internetResult.results.length} MCP servers via ${toolName}`,
          searchInfo: { tool: toolName, query: actualQuery }
        }])
      } else if (!internetResult.tavilyAvailable) {
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: internetResult.message || 'No web search tool configured.'
        }])
      } else {
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `No MCP servers found via ${toolName}`,
          searchInfo: { tool: toolName, query: actualQuery }
        }])
      }
    } catch (err: any) {
      console.error('Internet search failed:', err)
      setMessages(prev => [...prev, { 
        role: 'system', 
        content: `Internet search failed: ${err.message}`,
        isError: true
      }])
    } finally {
      setIsLoading(false)
    }
  }

  const handleSend = async () => {
    if (!input.trim() || isLoading) return

    const userMessage = input.trim()
    setInput('')
    setMessages(prev => [...prev, { role: 'user', content: userMessage }])
    setIsLoading(true)
    setStoreResults(null) // Clear previous store results



    // PRIORITY 0.25: Use LLM to classify user intent
    // This replaces naive regex detection - LLM can distinguish between:
    // - "create a flow using X mcp" (create_flow intent)
    // - "install X mcp server" (install_mcp intent)
    try {
      const intentResult = await classifyIntent(userMessage, tools)
      
      if (!intentResult.error && intentResult.intent) {
        
        // Handle extract_mcp_url intent
        if (intentResult.intent === 'extract_mcp_url' && intentResult.requirement) {
          setMessages(prev => [...prev, { role: 'system', content: `🔍 Extracting MCP server info from URL...` }])
          try {
            // Requirement is the URL
            const extractResult = await extractMCPServerFromURL(intentResult.requirement)
            
            if (extractResult.found && extractResult.mcpServer) {
              setInternetResults([extractResult.mcpServer])
              const toolInfo = extractResult.toolUsed ? ` via ${extractResult.toolUsed}` : ''
              
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `✅ Found MCP server${toolInfo}: ${extractResult.mcpServer.name}`
              }, {
                role: 'widget_internet',
                data: [extractResult.mcpServer],
                content: ''
              }])
            } else {
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: extractResult.message || 'No MCP server found at this URL.'
              }])
            }
          } catch (err: any) {
            console.error('URL extraction failed:', err)
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `URL extraction failed: ${err.message}`,
              isError: true
            }])
          } finally {
            setIsLoading(false)
          }
          return // Handled
        }

        // Handle install_mcp intent
        if (intentResult.intent === 'install_mcp' && intentResult.requirement) {
          setCurrentRequirement(intentResult.requirement)
          setOriginalRequest(userMessage)
          
          // First search the store
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `🔍 Searching MCP store for "${intentResult.requirement}"...`
          }])
          
          try {
            const storeResult = await searchToolsInStore(intentResult.requirement)
            
            if (storeResult.results && storeResult.results.length > 0) {
              setStoreResults(storeResult.results)
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `✅ Found ${storeResult.results.length} matching tools in the store:`
              }, { 
                role: 'widget_store', 
                data: storeResult.results,
                content: ''
              }])
            } else {
              // Not found in store, search internet
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `Not found in store. 🌐 Searching the internet...`
              }])
              
              const internetResult = await searchToolsOnInternet(intentResult.requirement)
              if (internetResult.results && internetResult.results.length > 0) {
                setInternetResults(internetResult.results)
                const toolInfo = internetResult.toolUsed ? ` via ${internetResult.toolUsed}` : ''
                setMessages(prev => [...prev, { 
                  role: 'system', 
                  content: `✅ Found ${internetResult.results.length} MCP servers online${toolInfo}:`
                }, { 
                  role: 'widget_internet', 
                  data: internetResult.results,
                  content: ''
                }])
              } else if (!internetResult.tavilyAvailable) {
                setMessages(prev => [...prev, { 
                  role: 'system', 
                  content: internetResult.message || 'No web search tool configured. Go to Settings → General to configure one.'
                }])
              } else {
                setMessages(prev => [...prev, { 
                  role: 'system', 
                  content: `No MCP servers found for "${intentResult.requirement}". Try a different search term or paste a GitHub URL.`
                }])
              }
            }
          } catch (err: any) {
            console.error('Install search failed:', err)
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `Search failed: ${err.message}`,
              isError: true
            }])
          } finally {
            setIsLoading(false)
          }
          return // Handled
        }
        
        // Handle browse_mcp_store intent
        if (intentResult.intent === 'browse_mcp_store') {
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `🔍 Browsing available MCP tools...`
          }])
          
          try {
            const storeResult = await searchToolsInStore(intentResult.requirement || 'popular tools')
            if (storeResult.results && storeResult.results.length > 0) {
              setStoreResults(storeResult.results)
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `✅ Found ${storeResult.results.length} tools available:`
              }])
            } else {
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `No tools found. Check Settings → MCP Store to browse all available servers.`
              }])
            }
          } catch (err: any) {
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `Browse failed: ${err.message}`,
              isError: true
            }])
          } finally {
            setIsLoading(false)
          }
          return // Handled
        }
        
        // Handle search_mcp_internet intent
        if (intentResult.intent === 'search_mcp_internet' && intentResult.requirement) {
          setCurrentRequirement(intentResult.requirement)
          
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `🌐 Searching the internet for "${intentResult.requirement}" MCP servers...`
          }])
          
          try {
            const internetResult = await searchToolsOnInternet(intentResult.requirement)
            if (internetResult.results && internetResult.results.length > 0) {
              setInternetResults(internetResult.results)
              const toolInfo = internetResult.toolUsed ? ` via ${internetResult.toolUsed}` : ''
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `✅ Found ${internetResult.results.length} MCP servers${toolInfo}:`
              }])
            } else if (!internetResult.tavilyAvailable) {
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: internetResult.message || 'No web search tool configured.'
              }])
            } else {
              setMessages(prev => [...prev, { 
                role: 'system', 
                content: `No MCP servers found online for that search.`
              }])
            }
          } catch (err: any) {
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `Internet search failed: ${err.message}`,
              isError: true
            }])
          } finally {
            setIsLoading(false)
          }
          return // Handled
        }
        
        // For create_flow and general_question, continue to AI chat below
        // (No return, let it fall through to the AI chat call)
      }
    } catch (err: any) {
      console.error('Intent classification failed, falling back to AI chat:', err)
      // Continue to AI chat on classification error
    }



    // PRIORITY 0.5: Check if user is providing refinement feedback on internet search results
    // When internet results are shown and user provides feedback, refine the search
    if (internetResults && internetResults.length > 0 && currentRequirement) {
      const refinementHint = detectSearchRefinementFeedback(userMessage)
      if (refinementHint) {
        // Combine original requirement with user's refinement feedback
        const refinedQuery = `${currentRequirement} ${refinementHint}`
        setCurrentRequirement(refinedQuery) // Update requirement for future refinements
        
        try {
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `Refining search with your feedback...`
          }])
          
          const internetResult = await searchToolsOnInternet(refinedQuery)
          if (internetResult.results && internetResult.results.length > 0) {
            setInternetResults(internetResult.results)
            const toolInfo = internetResult.toolUsed ? ` (via ${internetResult.toolUsed})` : ''
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `✅ Found ${internetResult.results.length} MCP servers with refined search${toolInfo}!`
            }])
          } else {
            const toolInfo = internetResult.toolUsed ? ` (searched via ${internetResult.toolUsed})` : ''
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `No additional servers found with that refinement${toolInfo}. Try different keywords.`
            }])
          }
        } catch (err: any) {
          console.error('Refined search failed:', err)
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `Refined search failed: ${err.message}`,
            isError: true
          }])
        } finally {
          setIsLoading(false)
        }
        return // Don't send to AI, we handled it
      }
    }

    // PRIORITY 1: Check if user is confirming internet search (after seeing "search for MCP servers online?")
    // This must come BEFORE the general store search confirmation to avoid loops
    const lastSystemMsg = messages.filter(m => m.role === 'system').slice(-1)[0]
    if (lastSystemMsg?.content?.includes('search for MCP servers online') && 
        isUserConfirmingToolSearch(userMessage) && 
        currentRequirement) {
      // User confirmed internet search - show searching placeholder
      setMessages(prev => [...prev, { 
        role: 'system', 
        content: `🔍 Searching for MCP servers...`,
        isSearching: true
      }])
      try {
        const internetResult = await searchToolsOnInternet(currentRequirement)
        // Update the searching message with actual tool and query info
        const toolName = internetResult.toolUsed || 'websearch'
        const actualQuery = internetResult.searchQuery || currentRequirement
        
        if (internetResult.results && internetResult.results.length > 0) {
          setInternetResults(internetResult.results)
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `✅ Found ${internetResult.results.length} MCP servers via ${toolName}`,
            searchInfo: { tool: toolName, query: actualQuery }
          }])
        } else if (!internetResult.tavilyAvailable) {
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: internetResult.message || 'No web search tool configured. Go to Settings → General to configure one.'
          }])
        } else {
          const toolInfo = internetResult.toolUsed ? ` (searched via ${internetResult.toolUsed})` : ''
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `No MCP servers found online for this requirement${toolInfo}.`
          }])
        }
      } catch (err: any) {
        console.error('Internet search failed:', err)
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `Internet search failed: ${err.message}`,
          isError: true
        }])
      } finally {
        setIsLoading(false)
      }
      return // Don't send to AI, we handled it
    }

    // PRIORITY 2: Check if user is confirming a tool search (either with pending or late confirmation)
    if (isUserConfirmingToolSearch(userMessage)) {
      let requirement = pendingToolRequirement
      
      // If no pending requirement, try to extract from recent AI messages
      if (!requirement) {
        // Look through recent messages for tool offers
        for (let i = messages.length - 1; i >= 0 && i >= messages.length - 10; i--) {
          const msg = messages[i]
          if (msg.role === 'assistant') {
            const toolOffer = detectToolOfferFromAI(msg.content)
            if (toolOffer) {
              requirement = toolOffer.requirement
              // Also restore originalRequest from the first user message in conversation
              if (!originalRequest && messages.length > 0) {
                const firstUserMsg = messages.find(m => m.role === 'user')
                if (firstUserMsg) {
                  setOriginalRequest(firstUserMsg.content)
                }
              }
              break
            }
          }
        }
      }
      
      if (requirement) {
        // User confirmed - search for tools now
        setCurrentRequirement(requirement) // Save requirement for internet search
        setInternetResults(null) // Clear internet results
        try {
          const searchResult = await searchToolsInStore(requirement)
          if (searchResult.results && searchResult.results.length > 0) {
            setStoreResults(searchResult.results.filter((r: any) => r.installable))
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `Found ${searchResult.results.length} matching tools! See options below.`
            }])
          } else {
            // No store results - offer internet search
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: 'No matching tools found in the MCP store. Would you like me to search for MCP servers online?'
            }])
            // Store the requirement for potential internet search
            setPendingToolRequirement(requirement)
          }
        } catch (err: any) {
          console.error('Store search failed:', err)
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: 'Failed to search the store.',
            isError: true
          }])
        } finally {
          setPendingToolRequirement(null) // Clear pending
          setIsLoading(false)
        }
        return // Don't send to AI, we handled it
      }
    }
    
    // Check if user is rejecting store results and wants internet search
    if ((storeResults && storeResults.length > 0 || currentRequirement) && 
        isUserRequestingInternetSearch(userMessage)) {
      // User rejected store results or asked for internet search
      const requirement = currentRequirement || pendingToolRequirement
      if (requirement) {
        setStoreResults(null) // Clear store results
        // Show searching message immediately
        setMessages(prev => [...prev, { 
          role: 'system', 
          content: `🔍 Searching for MCP servers...`,
          isSearching: true
        }])
        try {
          const internetResult = await searchToolsOnInternet(requirement)
          const toolName = internetResult.toolUsed || 'websearch'
          const actualQuery = internetResult.searchQuery || requirement
          
          if (internetResult.results && internetResult.results.length > 0) {
            setInternetResults(internetResult.results)
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `✅ Found ${internetResult.results.length} MCP servers via ${toolName}`,
              searchInfo: { tool: toolName, query: actualQuery }
            }])
          } else if (!internetResult.tavilyAvailable) {
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: internetResult.message || 'No web search tool configured. Go to Settings → General to configure one.'
            }])
          } else {
            setMessages(prev => [...prev, { 
              role: 'system', 
              content: `No MCP servers found via ${toolName}`,
              searchInfo: { tool: toolName, query: actualQuery }
            }])
          }
        } catch (err: any) {
          console.error('Internet search failed:', err)
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: `Internet search failed: ${err.message}`,
            isError: true
          }])
        } finally {
          setIsLoading(false)
        }
        return // Don't send to AI, we handled it
      }
    }
    
    // Clear pending if user sends something else
    if (pendingToolRequirement) {
      setPendingToolRequirement(null)
    }

    try {
      // Build history for API (exclude current message)
      const history = messages.map(m => ({ role: m.role, content: m.content }))
      
      // Start streaming response
      setMessages(prev => [...prev, { role: 'assistant', content: '', isStreaming: true }])
      
      let assistantContent = ''
      let finalResponse: Record<string, any> | null = null
      
      await sendChatMessageStream(
        userMessage,
        context,
        currentYaml,
        selectedNodes,
        history,
        (type, data) => {
          if (type === 'chunk') {
            // Robust deduplication: check if backend is sending full text or deltas
            if (assistantContent.length > 0 && (data.content as string).startsWith(assistantContent)) {
                assistantContent = data.content as string // Accumulation detected, replace
            } else {
                assistantContent += data.content as string // Delta detected, append
            }
            
            setMessages(prev => {
              const lastIdx = prev.length - 1
              const lastMsg = prev[lastIdx]
              
              if (lastMsg?.isStreaming) {
                // Update existing streaming message
                const newMsg: ChatMessage = { ...lastMsg, content: assistantContent }
                const newArr = [...prev]
                newArr[lastIdx] = newMsg
                return newArr
              } else {
                // Previous message was closed (e.g. by tool), start new one
                return [...prev, { role: 'assistant', content: assistantContent, isStreaming: true }]
              }
            })
          } else if (type === 'tool_start') {
            // Capture requirement for "Search Internet" link
            if ((data.name as string).startsWith('search_mcp') && (data.args as any)?.query) {
                setCurrentRequirement((data.args as any).query)
            }
            
            setMessages(prev => {
              // Close previous streaming message
              const copy = [...prev]
              if (copy[copy.length-1]?.isStreaming) {
                 copy[copy.length-1] = { ...copy[copy.length-1], isStreaming: false }
              }
              // Add tool banner
              return [...copy, { role: 'system', content: `🔨 Executing: ${data.name}...` }]
            })
            assistantContent = '' // Reset buffer
          } else if (type === 'tool_end') {
            // Check for structured data to check for UI visualization
            if (data.result_data) {
                if (data.name === 'search_mcp_store' && (data.result_data as any[]).length > 0) {
                    // Inject widget message
                    setMessages(prev => [...prev, { role: 'widget_store', data: data.result_data, content: '' }])
                } else if (data.name === 'search_mcp_internet' && (data.result_data as any[]).length > 0) {
                    // Inject widget message
                    setMessages(prev => [...prev, { role: 'widget_internet', data: data.result_data, content: '' }])
                }
            }


            let resultInfo = 'Completed.'
            if (data.result && ((data.result as string).includes('found 0') || (data.result as string).includes('No MCP'))) {
                resultInfo = 'Found 0 results.'
            } else if (data.result && (data.result as string).includes('Found')) {
                // Extract count usually "Found X..."
                const match = (data.result as string).match(/Found \d+/)
                if (match) resultInfo = `${match[0]} results.`
                else resultInfo = 'Found results.'
            }
            setMessages(prev => [...prev, { role: 'system', content: `✅ ${resultInfo}` }])
          } else if (type === 'status') {
            setMessages(prev => [...prev, { role: 'system', content: `ℹ️ ${data.message}` }])
          } else if (type === 'error') {
             setMessages(prev => [...prev, { role: 'assistant', content: `Error: ${data.error}`, isError: true }])
          } else if (type === 'complete') {
             finalResponse = data
          }
        }
      )

      if (finalResponse) {
        // Finalize state
        setMessages(prev => {
           const copy = [...prev]
           const lastIdx = copy.length - 1
           if (copy[lastIdx]?.isStreaming) {
               copy[lastIdx] = { ...copy[lastIdx], isStreaming: false }
           }
           // Attach YAML/Action to the very last message so UI renders preview
           // Note: The last message might be a system message if tool ended last.
           // We should attach to the last ASSISTANT message preferably, or just last message.
           // Usually flow ends with text.
           if (copy[lastIdx]) {
               copy[lastIdx] = { ...copy[lastIdx], proposedYaml: (finalResponse as Record<string, any>).proposedYaml, action: (finalResponse as Record<string, any>).action }
           }
           return copy
        })
        
        const response = {
            message: (finalResponse as Record<string, any>).message, // Includes logs? We ignore for prompt history building usually
            proposedYaml: (finalResponse as Record<string, any>).proposedYaml
        }

        
        // Check if AI mentioned missing tools
        const toolOffer = detectToolOfferFromAI(response.message)
        if (toolOffer) {
          // Store the original request so we can retry after tool installation
          setOriginalRequest(userMessage)
          
          if (toolOffer.isAskingUser) {
            // AI is asking user for confirmation - store the requirement for later
            setPendingToolRequirement(toolOffer.requirement)
            // Don't search yet, wait for user to say "yes"
          } else {
            // AI mentioned missing tools but didn't ask - auto-search
            try {
              const searchResult = await searchToolsInStore(toolOffer.requirement)
              if (searchResult.results && searchResult.results.length > 0) {
                setStoreResults(searchResult.results.filter((r: any) => r.installable))
              }
            } catch (err: any) {
              console.error('Store search failed:', err)
            }
          }
        }
        
        // Auto-apply YAML changes when received
        if (response.proposedYaml && onApplyYaml) {
          onApplyYaml(response.proposedYaml)
          setMessages(prev => [...prev, { 
            role: 'system', 
            content: '✓ Changes applied! Use Undo (⌘Z) to revert if needed.' 
          }])
        }
      }
    } catch (err: any) {
      setMessages(prev => [...prev, { 
        role: 'assistant', 
        content: `Network error: ${err.message}`,
        isError: true 
      }])
    } finally {
      setIsLoading(false)
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  const getContextTitle = () => {
    switch (context) {
      case 'create_flow': return 'Create Flow'
      case 'modify_flow': return 'Modify Flow'
      case 'modify_nodes': return 'Modify Nodes'
      case 'node_config': return 'Node Assistant'
      case 'multi_node': return 'Multi-Node Assistant'
      default: return 'AI Assistant'
    }
  }

  const getPlaceholder = () => {
    switch (context) {
      case 'create_flow': return 'Describe the flow you want to create...'
      case 'modify_flow': return 'Describe changes to make to this flow...'
      case 'modify_nodes': return 'What changes do you want to make?'
      case 'node_config': return 'How can I help with this node?'
      case 'multi_node': return 'What would you like to do with these nodes?'
      default: return 'Ask me anything about your flow...'
    }
  }

  if (!isOpen) return null

  return (
    <div className={`fixed bottom-4 right-4 bg-[var(--bg-secondary)] border border-[var(--border-color)] rounded-lg shadow-2xl flex flex-col z-50 transition-all duration-200 ${isExpanded ? 'w-[600px] h-[80vh]' : 'w-96 h-[500px]'}`}>
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-[var(--border-color)] bg-gradient-to-r from-primary/20 to-[var(--accent3)]/20">
        <div className="flex items-center gap-2 flex-1 min-w-0">
          <Sparkles size={18} className="text-primary flex-shrink-0" />
          <span className="font-semibold text-[var(--text-primary)] truncate">{getContextTitle()}</span>
          {/* Show focused node badge */}
          {focusedNode && (
            <span className="ml-2 px-2 py-0.5 text-xs rounded bg-primary/30 text-primary truncate">
              {focusedNode.name} ({focusedNode.type})
            </span>
          )}
          {/* Show selection badge */}
          {context === 'multi_node' && selectedNodes.length > 0 && (
            <span className="ml-2 px-2 py-0.5 text-xs rounded bg-blue-600/30 text-blue-300">
              {selectedNodes.length} nodes
            </span>
          )}
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          <button 
            onClick={() => setIsExpanded(!isExpanded)}
            className="p-1 hover:bg-white/10 rounded transition-colors"
            title={isExpanded ? 'Collapse' : 'Expand'}
          >
            {isExpanded ? (
              <Minimize2 size={16} className="text-[var(--text-secondary)]" />
            ) : (
              <Maximize2 size={16} className="text-[var(--text-secondary)]" />
            )}
          </button>
          <button 
            onClick={onClose}
            className="p-1 hover:bg-white/10 rounded transition-colors"
          >
            <X size={18} className="text-[var(--text-secondary)]" />
          </button>
        </div>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {messages.length === 0 && (
          <div className="text-center py-4">
            <Sparkles size={32} className="mx-auto mb-3 text-primary opacity-50" />
            
            {context === 'node_config' && focusedNode ? (
              <>
                <p className="text-[var(--text-primary)] font-medium mb-1">Node Refiner</p>
                <p className="text-[var(--text-secondary)] text-sm mb-4">
                  Let me help you improve <span className="text-primary font-medium">{focusedNode.name}</span>
                </p>
                
                {/* Node-specific suggestions */}
                <div className="text-left space-y-2">
                  <p className="text-xs text-[var(--text-muted)] mb-2">Try asking:</p>
                  {focusedNode.type === 'llm' && [
                    'Improve the system prompt',
                    'Make the prompt more concise',
                    'Add user_message to show output',
                    'Suggest tools for this task',
                  ].map((example, idx) => (
                    <button
                      key={idx}
                      onClick={() => setInput(example)}
                      className="block w-full text-left px-3 py-2 text-xs bg-[var(--bg-primary)] hover:bg-primary/20 rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                    >
                      → {example}
                    </button>
                  ))}
                  {focusedNode.type === 'input' && [
                    'Make the prompt clearer',
                    'Add options for choices',
                    'Improve user experience',
                  ].map((example, idx) => (
                    <button
                      key={idx}
                      onClick={() => setInput(example)}
                      className="block w-full text-left px-3 py-2 text-xs bg-[var(--bg-primary)] hover:bg-primary/20 rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                    >
                      → {example}
                    </button>
                  ))}
                  {(focusedNode.type !== 'llm' && focusedNode.type !== 'input') && [
                    'How can I improve this node?',
                    'What should I configure here?',
                    'Show me best practices',
                  ].map((example, idx) => (
                    <button
                      key={idx}
                      onClick={() => setInput(example)}
                      className="block w-full text-left px-3 py-2 text-xs bg-[var(--bg-primary)] hover:bg-primary/20 rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                    >
                      → {example}
                    </button>
                  ))}
                </div>
              </>
            ) : (
              <>
                <p className="text-[var(--text-secondary)] text-sm mb-4">
                  {context === 'modify_flow' 
                    ? 'I can help you modify and improve this flow'
                    : 'I can help you design and build flows'}
                </p>
                
                {/* Quick Examples based on context */}
                <div className="text-left space-y-2">
                  <p className="text-xs text-[var(--text-muted)] mb-2">Try an example:</p>
                  {context === 'modify_flow' ? [
                    'Add a new node to save the result',
                    'Insert an input step before processing',
                    'Add error handling to the flow',
                    'Add a confirmation step at the end',
                  ].map((example, idx) => (
                    <button
                      key={idx}
                      onClick={() => setInput(example)}
                      className="block w-full text-left px-3 py-2 text-xs bg-[var(--bg-primary)] hover:bg-primary/20 rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                    >
                      → {example}
                    </button>
                  )) : [
                    'Create a simple Q&A chatbot',
                    'Build a web search summarizer',
                    'Make a file reader and analyzer',
                    'Create a multi-step reasoning flow',
                  ].map((example, idx) => (
                    <button
                      key={idx}
                      onClick={() => setInput(example)}
                      className="block w-full text-left px-3 py-2 text-xs bg-[var(--bg-primary)] hover:bg-primary/20 rounded-lg transition-colors text-[var(--text-secondary)] hover:text-[var(--text-primary)]"
                    >
                      → {example}
                    </button>
                  ))}
                </div>
              </>
            )}
          </div>
        )}
        
        {messages.map((msg, idx) => {
          if (msg.role === 'widget_store') {
             return (
               <div key={idx} className="w-full my-4 pl-0 pr-2">
                  <StoreResultsPanel 
                    storeResults={msg.data}
                    installingTool={installingTool}
                    onInstall={handleInstallTool}
                    onSearchOnline={currentRequirement ? handleSearchOnline : null}
                  />
               </div>
             )
          }
          if (msg.role === 'widget_internet') {
             return (
               <div key={idx} className="w-full my-4 pl-0 pr-2">
                  <InternetResultsPanel 
                    results={msg.data}
                    onClear={() => setInternetResults(null)}
                    onInstall={handleInstallInternetMCP}
                    installingTool={installingTool}
                  />
               </div>
             )
          }
          
          return (
          <div 
            key={idx} 
            className={`flex ${msg.role === 'user' ? 'justify-end' : 'justify-start'}`}
          >
            <div 
              className={`max-w-[85%] px-3 py-2 rounded-lg text-sm ${
                msg.role === 'user' 
                  ? 'bg-primary text-white' 
                  : msg.role === 'system'
                  ? 'bg-green-600/20 text-green-400 text-center w-full'
                  : msg.isError
                  ? 'bg-red-600/20 text-red-400'
                  : 'bg-[var(--bg-primary)] text-[var(--text-primary)]'
              }`}
            >
              <div className="whitespace-pre-wrap break-words">
                {/* Render message content, removing YAML blocks for cleaner display */}
                {msg.content.split('```yaml')[0].trim() || msg.content}
              </div>
              
              {/* Show YAML indicator if present */}
              {msg.proposedYaml && (
                <div className="mt-2 pt-2 border-t border-white/10 text-xs flex items-center gap-1 text-primary">
                  <Check size={12} />
                  YAML generated
                </div>
              )}
            </div>
          </div>
          )
        })}
        
        {isLoading && (
          <div className="flex justify-start">
            <div className="bg-[var(--bg-primary)] px-3 py-2 rounded-lg">
              <Loader2 size={16} className="animate-spin text-primary" />
            </div>
          </div>
        )}
        
        {/* Panels now rendered inline in messages map */}
        
        <div ref={messagesEndRef} />
      </div>

      {/* Input */}
      <div className="p-3 border-t border-[var(--border-color)]">
        <div className="flex gap-2">
          <textarea
            ref={inputRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={getPlaceholder()}
            rows={isExpanded ? 6 : 1}
            className={`flex-1 px-3 py-2 bg-[var(--bg-primary)] border border-[var(--border-color)] rounded-lg text-sm text-[var(--text-primary)] placeholder:text-[var(--text-secondary)] focus:outline-none focus:ring-2 focus:ring-primary/50 ${isExpanded ? 'resize-y min-h-[120px]' : 'resize-none'}`}
            disabled={isLoading}
          />
          <button
            onClick={handleSend}
            disabled={!input.trim() || isLoading}
            className="px-3 py-2 bg-primary hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed rounded-lg transition-colors"
          >
            <Send size={16} className="text-white" />
          </button>
        </div>
      </div>
    </div>
  )
}
